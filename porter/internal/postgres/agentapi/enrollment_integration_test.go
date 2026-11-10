package agentapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
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

func TestPostgresSingleUseAgentEnrollmentIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_AGENTAPI_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_AGENTAPI_TEST_POSTGRES_DSN to a disposable porter_test_agentapi_* database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_agentapi_") {
		t.Fatalf("refusing destructive integration test in database %q", databaseName)
	}
	if err := resetAgentAPIIntegrationDB(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetAgentAPIIntegrationDB(cleanupCtx, db); err != nil {
			t.Errorf("reset disposable enrollment test database: %v", err)
		}
	})
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply schema through migration 0009: %v", err)
	}
	repo, err := pgstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	handler, err := NewWithConfig(repo, "operator-secret", Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()
	client := tlsServer.Client()

	issueBody, _ := json.Marshal(v1.CreateAgentEnrollmentTokenRequest{Target: v1.AgentEnrollmentRemoteHost, ControlPlaneURL: tlsServer.URL})
	unauthorized := postAgentJSON(t, client, tlsServer.URL+"/internal/admin/fleet/enrollment-tokens", issueBody, "wrong-operator-token", "")
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated operator token issue status=%d", unauthorized.StatusCode)
	}
	issueResponse := postAgentJSON(t, client, tlsServer.URL+"/internal/admin/fleet/enrollment-tokens", issueBody, "operator-secret", "")
	defer issueResponse.Body.Close()
	if issueResponse.StatusCode != http.StatusCreated || issueResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("operator token issue status=%d cache=%q", issueResponse.StatusCode, issueResponse.Header.Get("Cache-Control"))
	}
	var issue v1.AgentEnrollmentTokenIssue
	if err := json.NewDecoder(issueResponse.Body).Decode(&issue); err != nil || issue.Token == "" || issue.Target != v1.AgentEnrollmentRemoteHost {
		t.Fatalf("token issue=%+v err=%v", issue, err)
	}

	var storedHash []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM porter_control_plane.agent_enrollment_tokens WHERE id = $1`, issue.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	wantEnrollmentHash := sha256.Sum256([]byte(issue.Token))
	if !equalBytes(storedHash, wantEnrollmentHash[:]) {
		t.Fatal("database did not store the SHA-256 enrollment-token hash")
	}
	var plaintextColumns int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'porter_control_plane' AND table_name = 'agent_enrollment_tokens'
  AND column_name IN ('token', 'bearer_token', 'secret')`).Scan(&plaintextColumns); err != nil || plaintextColumns != 0 {
		t.Fatalf("enrollment table plaintext token columns=%d err=%v", plaintextColumns, err)
	}

	expired, err := repo.CreateAgentEnrollmentTokenAt(ctx, v1.AgentEnrollmentSameHost, tlsServer.URL, now.Add(-2*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("create expired same-host token: %v", err)
	}
	enrollBody, _ := json.Marshal(protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: "remote-worker-01"})
	expiredResponse := postAgentJSON(t, client, tlsServer.URL+"/internal/agent/enroll", enrollBody, expired.Token, "")
	expiredResponse.Body.Close()
	if expiredResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired enrollment token status=%d", expiredResponse.StatusCode)
	}

	enrollResponse := postAgentJSON(t, client, tlsServer.URL+"/internal/agent/enroll", enrollBody, issue.Token, "")
	defer enrollResponse.Body.Close()
	if enrollResponse.StatusCode != http.StatusCreated || enrollResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("enrollment redemption status=%d cache=%q", enrollResponse.StatusCode, enrollResponse.Header.Get("Cache-Control"))
	}
	var enrolled protocol.AgentEnrollmentResponseV2
	if err := json.NewDecoder(enrollResponse.Body).Decode(&enrolled); err != nil {
		t.Fatal(err)
	}
	if err := enrolled.Validate(); err != nil || enrolled.EnrollmentTarget != string(v1.AgentEnrollmentRemoteHost) || enrolled.ControlPlaneURL != tlsServer.URL {
		t.Fatalf("enrolled identity=%+v err=%v", enrolled, err)
	}
	var nodeName, nodeState string
	var nodeTarget v1.AgentEnrollmentTarget
	if err := db.QueryRowContext(ctx, `SELECT name, state, enrollment_target FROM porter_control_plane.fleet_nodes WHERE id = $1`, enrolled.NodeID).Scan(&nodeName, &nodeState, &nodeTarget); err != nil {
		t.Fatal(err)
	}
	if nodeName != "remote-worker-01" || nodeState != string(v1.FleetNodeOffline) || nodeTarget != v1.AgentEnrollmentRemoteHost {
		t.Fatalf("enrolled Fleet node name=%q state=%q target=%q", nodeName, nodeState, nodeTarget)
	}
	var nodeCredentialHash []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM porter_control_plane.agent_node_credentials WHERE node_id = $1 AND generation = 1`, enrolled.NodeID).Scan(&nodeCredentialHash); err != nil {
		t.Fatal(err)
	}
	wantNodeHash := sha256.Sum256([]byte(enrolled.NodeToken))
	if !equalBytes(nodeCredentialHash, wantNodeHash[:]) {
		t.Fatal("node credential was not stored as its SHA-256 hash")
	}
	var tokenBinding string
	if err := db.QueryRowContext(ctx, `SELECT fleet_node_id FROM porter_control_plane.agent_enrollment_tokens WHERE id = $1`, issue.ID).Scan(&tokenBinding); err != nil || tokenBinding != enrolled.NodeID {
		t.Fatalf("enrollment token binding=%q err=%v", tokenBinding, err)
	}

	replay := postAgentJSON(t, client, tlsServer.URL+"/internal/agent/enroll", enrollBody, issue.Token, "")
	replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed one-time enrollment token status=%d", replay.StatusCode)
	}
	var nodeCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.fleet_nodes WHERE enrollment_target = 'remote_host'`).Scan(&nodeCount); err != nil || nodeCount != 1 {
		t.Fatalf("replay created additional Fleet nodes=%d err=%v", nodeCount, err)
	}
	var secretInEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE data::text LIKE '%' || $1 || '%'`, issue.Token).Scan(&secretInEvents); err != nil || secretInEvents != 0 {
		t.Fatalf("plaintext enrollment token leaked into events: matches=%d err=%v", secretInEvents, err)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
