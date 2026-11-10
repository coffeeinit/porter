package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"
	pgstore "porter/controlplane/internal/postgres/store"
	"porter/protocol"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMountedPGEnrollmentRecoveryFlow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_STORE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_STORE_TEST_POSTGRES_DSN to a disposable porter_test_store_* database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to disposable PostgreSQL database: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_store_") {
		t.Fatalf("refusing destructive recovery test in database %q; name must start with porter_test_store_", databaseName)
	}
	if err := resetAPIMountTestDB(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetAPIMountTestDB(cleanupCtx, db); err != nil {
			t.Errorf("reset disposable recovery test database: %v", err)
		}
	})
	if handler, closeDB, err := configureAPIRoutes("operator-token", dsn); err == nil {
		_ = handler
		closeDB()
		t.Fatal("server mounted an un-migrated PostgreSQL database")
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('porter_control_plane.projects') IS NOT NULL`).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatal("server startup automatically applied PostgreSQL migrations")
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply PostgreSQL migrations explicitly: %v", err)
	}

	var applicationLogs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&applicationLogs)
	defer log.SetOutput(previousLogWriter)

	server, closeStore, mountedHandler := startPostgresAPITestServer(t, dsn)
	client := server.Client()
	operatorToken := "operator-token"
	for _, invalidAdmin := range []string{"", "wrong-admin-token"} {
		capabilityProbe := doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/capabilities", nil, invalidAdmin)
		assertMountedAPIStatus(t, capabilityProbe, http.StatusUnauthorized, "capability probe")
		fleetList := doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/nodes", nil, invalidAdmin)
		assertMountedAPIStatus(t, fleetList, http.StatusUnauthorized, "Fleet list")
	}
	capabilityProbe := doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/capabilities", nil, operatorToken)
	if capabilityProbe.StatusCode != http.StatusOK || capabilityProbe.Header.Get("Cache-Control") != "no-store" {
		capabilityProbe.Body.Close()
		t.Fatalf("authenticated capability probe status=%d", capabilityProbe.StatusCode)
	}
	var capabilities v1.FleetCapabilities
	if err := json.NewDecoder(capabilityProbe.Body).Decode(&capabilities); err != nil {
		capabilityProbe.Body.Close()
		t.Fatal("decode capability probe")
	}
	capabilityProbe.Body.Close()
	if capabilities != (v1.FleetCapabilities{JoinTokenIssuance: true, AgentEnrollment: true, NodeCredentialRotation: true}) {
		t.Fatalf("capability flags do not match mounted routes: %+v", capabilities)
	}
	assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/capabilities?token=probe", nil, operatorToken), http.StatusBadRequest, "capability query-token rejection")
	plainEnrollment := httptest.NewRecorder()
	mountedHandler.ServeHTTP(plainEnrollment, httptest.NewRequest(http.MethodPost, "http://porter.local/api/v1/fleet/enroll", strings.NewReader(`{"protocol_version":"v2","hostname":"plain-http-must-fail"}`)))
	if plainEnrollment.Code != http.StatusUpgradeRequired {
		t.Fatalf("plaintext enrollment status=%d, want 426", plainEnrollment.Code)
	}

	issueBody, err := json.Marshal(v1.CreateAgentEnrollmentTokenRequest{
		Target: v1.AgentEnrollmentRemoteHost, ControlPlaneURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalidAdmin := range []string{"", "wrong-admin-token"} {
		assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/join-tokens", issueBody, invalidAdmin), http.StatusUnauthorized, "join-token issuance")
	}
	assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/join-tokens?token=probe", issueBody, operatorToken), http.StatusBadRequest, "join-token query rejection")
	issue := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/join-tokens", issueBody, operatorToken)
	if issue.StatusCode != http.StatusCreated || issue.Header.Get("Cache-Control") != "no-store" {
		issue.Body.Close()
		t.Fatalf("join token issue status=%d cache=%q", issue.StatusCode, issue.Header.Get("Cache-Control"))
	}
	var issued v1.AgentEnrollmentTokenIssue
	if err := json.NewDecoder(issue.Body).Decode(&issued); err != nil {
		issue.Body.Close()
		t.Fatal(err)
	}
	issue.Body.Close()
	if !strings.HasPrefix(issued.Token, "ptenr_") {
		t.Fatalf("join token has unexpected format")
	}
	if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(issued.Token, "ptenr_")); err != nil || len(raw) != 32 {
		t.Fatalf("join token is not 256-bit raw-base64url: decoded bytes=%d err=%v", len(raw), err)
	}
	if remaining := time.Until(issued.ExpiresAt); remaining < 9*time.Minute || remaining > 10*time.Minute+time.Second {
		t.Fatalf("join token lifetime remaining=%s, want default 10 minutes", remaining)
	}

	const hostname = "recovery-worker-01"
	enrollmentStarted := time.Now().UTC()
	enrollBody, err := json.Marshal(protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: hostname})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalidBearer := range []string{"", "not-a-join-token", operatorToken} {
		assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/enroll", enrollBody, invalidBearer), http.StatusUnauthorized, "agent enrollment")
	}
	assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/enroll?token=probe", enrollBody, issued.Token), http.StatusBadRequest, "enrollment query-token rejection")
	lost201 := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/enroll", enrollBody, issued.Token)
	if lost201.StatusCode != http.StatusCreated {
		lost201.Body.Close()
		t.Fatalf("first enrollment status=%d, want 201", lost201.StatusCode)
	}
	// Simulate the client losing the successful 201 and its node credential.
	lost201.Body.Close()

	replay := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/enroll", enrollBody, issued.Token)
	replayBody, readErr := readAndClose(replay)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if replay.StatusCode != http.StatusUnauthorized || strings.Contains(string(replayBody), issued.Token) {
		t.Fatalf("replay response status=%d leaked enrollment secret=%t", replay.StatusCode, strings.Contains(string(replayBody), issued.Token))
	}
	var replayError v1.ErrorResponse
	if err := json.Unmarshal(replayBody, &replayError); err != nil || replayError.Error.Code != v1.ErrorUnauthenticated || replayError.Error.RequestID == "" {
		t.Fatalf("replay error envelope=%+v err=%v", replayError.Error, err)
	}

	fleet := doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/nodes?page_size=100", nil, operatorToken)
	if fleet.StatusCode != http.StatusOK {
		fleet.Body.Close()
		t.Fatalf("Fleet list status=%d", fleet.StatusCode)
	}
	var page v1.Page[v1.FleetNode]
	if err := json.NewDecoder(fleet.Body).Decode(&page); err != nil {
		fleet.Body.Close()
		t.Fatal(err)
	}
	fleet.Body.Close()
	matches := make([]v1.FleetNode, 0, 1)
	for _, node := range page.Items {
		if node.Name == hostname && node.State == v1.FleetNodeOffline && node.EnrollmentTarget == v1.AgentEnrollmentRemoteHost && !node.CreatedAt.Before(enrollmentStarted.Add(-time.Second)) && !node.CreatedAt.After(time.Now().UTC().Add(time.Second)) {
			matches = append(matches, node)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("manual recovery lookup found %d matching offline Fleet rows for hostname %q", len(matches), hostname)
	}
	node := matches[0]

	rotationStarted := time.Now().UTC()
	for _, invalidAdmin := range []string{"", "wrong-admin-token"} {
		assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/nodes/"+node.ID+"/credentials", nil, invalidAdmin), http.StatusUnauthorized, "node credential rotation")
	}
	assertMountedAPIStatus(t, doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/nodes/"+node.ID+"/credentials?token=probe", nil, operatorToken), http.StatusBadRequest, "credential rotation query rejection")
	rotation := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/nodes/"+node.ID+"/credentials", nil, operatorToken)
	if rotation.StatusCode != http.StatusCreated || rotation.Header.Get("Cache-Control") != "no-store" {
		rotation.Body.Close()
		t.Fatalf("credential rotation status=%d cache=%q", rotation.StatusCode, rotation.Header.Get("Cache-Control"))
	}
	var credential pgstore.NodeCredentialIssue
	if err := json.NewDecoder(rotation.Body).Decode(&credential); err != nil {
		rotation.Body.Close()
		t.Fatal(err)
	}
	rotation.Body.Close()
	if credential.NodeID != node.ID || credential.Generation != 2 || credential.PreviousValidUntil == nil {
		t.Fatalf("rotated credential metadata: node_id=%q generation=%d has_previous_expiry=%t", credential.NodeID, credential.Generation, credential.PreviousValidUntil != nil)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(credential.Token); err != nil || len(raw) != 32 {
		t.Fatalf("rotated node token is not 256-bit raw-base64url: decoded bytes=%d err=%v", len(raw), err)
	}
	grace := credential.PreviousValidUntil.Sub(rotationStarted)
	if grace < 4*time.Minute || grace > 5*time.Minute+time.Second {
		t.Fatalf("previous credential grace=%s, want default 5 minutes", grace)
	}

	var storedEnrollmentHash []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM porter_control_plane.agent_enrollment_tokens WHERE id = $1`, issued.ID).Scan(&storedEnrollmentHash); err != nil {
		t.Fatal(err)
	}
	wantEnrollmentHash := sha256.Sum256([]byte(issued.Token))
	if !equalSecretHash(storedEnrollmentHash, wantEnrollmentHash[:]) {
		t.Fatal("PostgreSQL did not store only the SHA-256 enrollment token hash")
	}
	var plaintextColumns int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'porter_control_plane'
  AND table_name IN ('agent_enrollment_tokens', 'agent_node_credentials')
  AND column_name IN ('token', 'bearer_token', 'secret')`).Scan(&plaintextColumns); err != nil || plaintextColumns != 0 {
		t.Fatalf("PostgreSQL enrollment schema plaintext-secret columns=%d err=%v", plaintextColumns, err)
	}
	var generationOneExpiry time.Time
	if err := db.QueryRowContext(ctx, `SELECT expires_at FROM porter_control_plane.agent_node_credentials WHERE node_id = $1 AND generation = 1`, node.ID).Scan(&generationOneExpiry); err != nil {
		t.Fatal(err)
	}
	if !generationOneExpiry.Equal(credential.PreviousValidUntil.Truncate(time.Microsecond)) {
		t.Fatalf("database previous-token grace expiry=%s, response=%s", generationOneExpiry, credential.PreviousValidUntil)
	}
	heartbeatBody, err := json.Marshal(protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		bearer string
		nodeID string
	}{
		{name: "missing node credential", nodeID: node.ID},
		{name: "invalid node credential", bearer: "invalid-node-token", nodeID: node.ID},
		{name: "credential bound to wrong node", bearer: credential.Token, nodeID: "node_other"},
	} {
		resp := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/heartbeat", heartbeatBody, tc.bearer, tc.nodeID)
		assertMountedAPIStatus(t, resp, http.StatusUnauthorized, tc.name)
	}
	var generationTwoHash []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM porter_control_plane.agent_node_credentials WHERE node_id = $1 AND generation = 2`, node.ID).Scan(&generationTwoHash); err != nil {
		t.Fatal(err)
	}
	wantNodeHash := sha256.Sum256([]byte(credential.Token))
	if !equalSecretHash(generationTwoHash, wantNodeHash[:]) {
		t.Fatal("PostgreSQL did not store only the SHA-256 rotated node credential hash")
	}
	var leakedSecrets int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE data::text LIKE '%' || $1 || '%' OR data::text LIKE '%' || $2 || '%'`, issued.Token, credential.Token).Scan(&leakedSecrets); err != nil || leakedSecrets != 0 {
		t.Fatalf("enrollment or node token leaked into PostgreSQL events: count=%d err=%v", leakedSecrets, err)
	}

	heartbeat := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/heartbeat", heartbeatBody, credential.Token, node.ID)
	assertMountedAPIStatus(t, heartbeat, http.StatusNoContent, "matching node credential heartbeat")
	commands := doMountedAgentRequest(t, client, http.MethodGet, server.URL+"/internal/agent/commands", nil, credential.Token, node.ID)
	if commands.StatusCode != http.StatusOK {
		commands.Body.Close()
		t.Fatalf("authenticated mounted command poll status=%d", commands.StatusCode)
	}
	var noCommands []protocol.CommandV2
	if err := json.NewDecoder(commands.Body).Decode(&noCommands); err != nil || len(noCommands) != 0 {
		commands.Body.Close()
		t.Fatal("mounted command poll did not return an empty list")
	}
	commands.Body.Close()
	now := time.Now().UTC()
	telemetryBody, err := json.Marshal(protocol.TelemetryBatchV2{
		ProtocolVersion: protocol.VersionV2, NodeID: node.ID,
		Logs:    []protocol.TelemetryLogV2{{RecordID: "mounted-log-1", ReportedAt: now, Level: "info", Message: "agent online"}},
		Metrics: []protocol.TelemetryMetricV2{{RecordID: "mounted-metric-1", ReportedAt: now, Name: "node.cpu_ticks", Value: 1, Unit: "ticks"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/telemetry", telemetryBody, credential.Token, node.ID)
	assertMountedAPIStatus(t, telemetry, http.StatusNoContent, "authenticated mounted telemetry")
	wrongNodeTelemetryBody, err := json.Marshal(protocol.TelemetryBatchV2{
		ProtocolVersion: protocol.VersionV2, NodeID: "node_other",
		Logs: []protocol.TelemetryLogV2{{RecordID: "mounted-wrong-log", ReportedAt: now, Level: "info", Message: "should be rejected"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongTelemetry := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/telemetry", wrongNodeTelemetryBody, credential.Token, node.ID)
	assertMountedAPIStatus(t, wrongTelemetry, http.StatusForbidden, "telemetry body node mismatch")
	wrongNodeResultBody, err := json.Marshal(protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: "cmd_probe", OperationID: "op_probe", NodeID: "node_other",
		Attempt: 1, Kind: "vm.stop", LeaseToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)), Success: true, State: "stopped",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongResult := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/results", wrongNodeResultBody, credential.Token, node.ID)
	assertMountedAPIStatus(t, wrongResult, http.StatusForbidden, "command result body node mismatch")

	ready := doPostgresAPIRequest(t, client, http.MethodGet, server.URL+"/api/v1/fleet/nodes?page_size=100", nil, operatorToken)
	if ready.StatusCode != http.StatusOK {
		ready.Body.Close()
		t.Fatalf("post-heartbeat Fleet list status=%d", ready.StatusCode)
	}
	var readyPage v1.Page[v1.FleetNode]
	if err := json.NewDecoder(ready.Body).Decode(&readyPage); err != nil {
		ready.Body.Close()
		t.Fatal(err)
	}
	ready.Body.Close()
	if len(readyPage.Items) != 1 || readyPage.Items[0].ID != node.ID || readyPage.Items[0].State != v1.FleetNodeReady || readyPage.Items[0].LastSeenAt == nil {
		t.Fatalf("Fleet after authenticated heartbeat=%+v", readyPage.Items)
	}

	additionalIssueBody, err := json.Marshal(v1.CreateAgentEnrollmentTokenRequest{Target: v1.AgentEnrollmentSameHost, ControlPlaneURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	additionalIssue := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/join-tokens", additionalIssueBody, operatorToken)
	if additionalIssue.StatusCode != http.StatusCreated {
		additionalIssue.Body.Close()
		t.Fatalf("additional-node token issue status=%d", additionalIssue.StatusCode)
	}
	var additionalToken v1.AgentEnrollmentTokenIssue
	if err := json.NewDecoder(additionalIssue.Body).Decode(&additionalToken); err != nil {
		additionalIssue.Body.Close()
		t.Fatal(err)
	}
	additionalIssue.Body.Close()
	additionalEnrollBody, err := json.Marshal(protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: "additional-worker-01"})
	if err != nil {
		t.Fatal(err)
	}
	additionalEnroll := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/enroll", additionalEnrollBody, additionalToken.Token)
	if additionalEnroll.StatusCode != http.StatusCreated {
		additionalEnroll.Body.Close()
		t.Fatalf("additional-node enrollment status=%d", additionalEnroll.StatusCode)
	}
	var additionalNode protocol.AgentEnrollmentResponseV2
	if err := json.NewDecoder(additionalEnroll.Body).Decode(&additionalNode); err != nil {
		additionalEnroll.Body.Close()
		t.Fatal(err)
	}
	additionalEnroll.Body.Close()
	if additionalNode.NodeID == node.ID || additionalNode.State != string(v1.FleetNodeOffline) {
		t.Fatalf("additional node identity: node_id=%q state=%q; first node=%q", additionalNode.NodeID, additionalNode.State, node.ID)
	}
	additionalRotation := doPostgresAPIRequest(t, client, http.MethodPost, server.URL+"/api/v1/fleet/nodes/"+additionalNode.NodeID+"/credentials", []byte(`{}`), operatorToken)
	if additionalRotation.StatusCode != http.StatusCreated {
		additionalRotation.Body.Close()
		t.Fatalf("additional node credential rotation status=%d", additionalRotation.StatusCode)
	}
	var additionalCredential pgstore.NodeCredentialIssue
	if err := json.NewDecoder(additionalRotation.Body).Decode(&additionalCredential); err != nil {
		additionalRotation.Body.Close()
		t.Fatal("decode additional node credential rotation")
	}
	additionalRotation.Body.Close()
	if additionalCredential.NodeID != additionalNode.NodeID || additionalCredential.Generation != 2 {
		t.Fatal("additional node credential generation did not advance")
	}
	staleIssuedAt := time.Now().UTC().Add(-2 * time.Minute)
	staleExpiry := time.Now().UTC().Add(-time.Minute)
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.agent_node_credentials SET issued_at = $1, expires_at = $2, revoked_at = NULL WHERE node_id = $3 AND generation = 1`, staleIssuedAt, staleExpiry, additionalNode.NodeID); err != nil {
		t.Fatal(err)
	}
	staleCredential := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/heartbeat", heartbeatBody, additionalNode.NodeToken, additionalNode.NodeID)
	assertMountedAPIStatus(t, staleCredential, http.StatusUnauthorized, "expired prior-generation credential")
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.agent_node_credentials SET expires_at = $1, revoked_at = $2 WHERE node_id = $3 AND generation = 1`, time.Now().UTC().Add(time.Minute), time.Now().UTC(), additionalNode.NodeID); err != nil {
		t.Fatal(err)
	}
	revokedCredential := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/heartbeat", heartbeatBody, additionalNode.NodeToken, additionalNode.NodeID)
	assertMountedAPIStatus(t, revokedCredential, http.StatusUnauthorized, "revoked prior-generation credential")
	additionalHeartbeatBody, err := json.Marshal(protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	additionalHeartbeat := doMountedAgentRequest(t, client, http.MethodPost, server.URL+"/internal/agent/heartbeat", additionalHeartbeatBody, additionalCredential.Token, additionalNode.NodeID)
	assertMountedAPIStatus(t, additionalHeartbeat, http.StatusNoContent, "current additional-node credential heartbeat")
	currentCredentialPoll := doMountedAgentRequest(t, client, http.MethodGet, server.URL+"/internal/agent/commands", nil, additionalCredential.Token, additionalNode.NodeID)
	if currentCredentialPoll.StatusCode != http.StatusOK {
		currentCredentialPoll.Body.Close()
		t.Fatalf("current additional-node credential command poll status=%d", currentCredentialPoll.StatusCode)
	}
	currentCredentialPoll.Body.Close()

	server.Close()
	closeStore()
	server, closeStore, _ = startPostgresAPITestServer(t, dsn)
	persisted := doPostgresAPIRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/fleet/nodes?page_size=100", nil, operatorToken)
	defer persisted.Body.Close()
	if persisted.StatusCode != http.StatusOK {
		t.Fatalf("Fleet list after server restart status=%d", persisted.StatusCode)
	}
	var persistedPage v1.Page[v1.FleetNode]
	if err := json.NewDecoder(persisted.Body).Decode(&persistedPage); err != nil {
		t.Fatal(err)
	}
	states := make(map[string]v1.FleetNodeState, len(persistedPage.Items))
	for _, persistedNode := range persistedPage.Items {
		states[persistedNode.ID] = persistedNode.State
	}
	if len(states) != 2 || states[node.ID] != v1.FleetNodeReady || states[additionalNode.NodeID] != v1.FleetNodeReady {
		t.Fatalf("Fleet rows did not survive API restart: %+v", persistedPage.Items)
	}

	for _, secret := range []string{issued.Token, credential.Token, additionalToken.Token, additionalNode.NodeToken, additionalCredential.Token} {
		if strings.Contains(applicationLogs.String(), secret) {
			t.Fatal("a bearer secret was written to application logs")
		}
	}
	var leakedEventSecrets int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM porter_control_plane.resource_events
WHERE data::text LIKE '%' || $1 || '%'
   OR data::text LIKE '%' || $2 || '%'
   OR data::text LIKE '%' || $3 || '%'
   OR data::text LIKE '%' || $4 || '%'
   OR data::text LIKE '%' || $5 || '%'`, issued.Token, credential.Token, additionalToken.Token, additionalNode.NodeToken, additionalCredential.Token).Scan(&leakedEventSecrets); err != nil || leakedEventSecrets != 0 {
		t.Fatalf("bearer secret leaked into PostgreSQL audit events: count=%d err=%v", leakedEventSecrets, err)
	}
	server.Close()
	closeStore()
}

func doMountedAgentRequest(t *testing.T, client *http.Client, method, url string, body []byte, bearer, nodeID string) *http.Response {
	t.Helper()
	var requestBody *strings.Reader
	if body == nil {
		requestBody = strings.NewReader("")
	} else {
		requestBody = strings.NewReader(string(body))
	}
	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		t.Fatal("build mounted agent request")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if nodeID != "" {
		request.Header.Set("X-Porter-Node-ID", nodeID)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("mounted agent request failed")
	}
	return response
}

func assertMountedAPIStatus(t *testing.T, response *http.Response, expected int, label string) {
	t.Helper()
	status := response.StatusCode
	response.Body.Close()
	if status != expected {
		t.Fatalf("%s returned HTTP %d, want %d", label, status, expected)
	}
}

func readAndClose(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(response.Body)
	return buffer.Bytes(), err
}

func equalSecretHash(stored, expected []byte) bool {
	if len(stored) != len(expected) {
		return false
	}
	var diff byte
	for i := range stored {
		diff |= stored[i] ^ expected[i]
	}
	return diff == 0
}
