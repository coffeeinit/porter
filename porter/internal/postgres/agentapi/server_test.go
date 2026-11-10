package agentapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
	"porter/protocol"
)

type agentRepoStub struct {
	issue                pgstore.NodeCredentialIssue
	enrollmentIssue      v1.AgentEnrollmentTokenIssue
	enrollmentResponse   protocol.AgentEnrollmentResponseV2
	enrollmentErr        error
	enrollmentToken      string
	enrollmentRequest    protocol.AgentEnrollmentRequestV2
	authenticated        bool
	authErr              error
	heartbeatErr         error
	telemetryErr         error
	telemetryBatch       protocol.TelemetryBatchV2
	telemetryNodeID      string
	telemetryCalls       int
	requeueErr           error
	leaseErr             error
	resultErr            error
	commands             []protocol.CommandV2
	result               protocol.CommandResultV2
	heartbeatCalls       int
	requeueCalls         int
	leaseCalls           int
	resultCalls          int
	issueCalls           int
	enrollmentIssueCalls int
	enrollmentCalls      int
	leaseNodeID          string
	leaseLimit           int
}

func (s *agentRepoStub) CreateAgentEnrollmentToken(_ context.Context, _ v1.AgentEnrollmentTarget, _ string, _ time.Time) (v1.AgentEnrollmentTokenIssue, error) {
	s.enrollmentIssueCalls++
	return s.enrollmentIssue, nil
}
func (s *agentRepoStub) RedeemAgentEnrollmentToken(_ context.Context, token string, request protocol.AgentEnrollmentRequestV2, _ time.Time) (protocol.AgentEnrollmentResponseV2, error) {
	s.enrollmentCalls++
	s.enrollmentToken, s.enrollmentRequest = token, request
	return s.enrollmentResponse, s.enrollmentErr
}
func (s *agentRepoStub) ProvisionNodeCredential(context.Context, string, time.Time, time.Duration) (pgstore.NodeCredentialIssue, error) {
	s.issueCalls++
	return s.issue, nil
}
func (s *agentRepoStub) AuthenticateNodeCredential(_ context.Context, nodeID, token string, _ time.Time) (bool, error) {
	if s.authErr != nil {
		return false, s.authErr
	}
	return s.authenticated && nodeID == "node-a" && token == "node-token", nil
}
func (s *agentRepoStub) RecordAgentHeartbeat(context.Context, string, time.Time) error {
	s.heartbeatCalls++
	return s.heartbeatErr
}
func (s *agentRepoStub) IngestAgentTelemetry(_ context.Context, nodeID string, batch protocol.TelemetryBatchV2, _ time.Time) (pgstore.TelemetryIngestCounts, error) {
	s.telemetryCalls++
	s.telemetryNodeID, s.telemetryBatch = nodeID, batch
	return pgstore.TelemetryIngestCounts{LogsAccepted: len(batch.Logs), MetricsAccepted: len(batch.Metrics)}, s.telemetryErr
}
func (s *agentRepoStub) RequeueExpiredCommandsForNode(_ context.Context, nodeID string, _ time.Time, limit int) (int, error) {
	s.requeueCalls++
	s.leaseNodeID, s.leaseLimit = nodeID, limit
	return 0, s.requeueErr
}
func (s *agentRepoStub) LeaseProtocolCommandsAt(_ context.Context, nodeID string, limit int, _ time.Duration, _ time.Time) ([]protocol.CommandV2, error) {
	s.leaseCalls++
	s.leaseNodeID, s.leaseLimit = nodeID, limit
	return s.commands, s.leaseErr
}
func (s *agentRepoStub) RecordProtocolCommandResult(_ context.Context, result protocol.CommandResultV2) error {
	s.resultCalls++
	s.result = result
	return s.resultErr
}

func agentTestNow() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

func newAgentTestServer(t *testing.T, repo *agentRepoStub) (*Server, *httptest.Server) {
	t.Helper()
	server, err := NewWithConfig(repo, "operator-secret", Config{Now: agentTestNow})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(server)
	t.Cleanup(ts.Close)
	return server, ts
}

func doAgentTLS(t *testing.T, ts *httptest.Server, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func validAgentV2Command() protocol.CommandV2 {
	return protocol.CommandV2{
		ProtocolVersion: protocol.VersionV2, ID: "cmd-a", NodeID: "node-a", OperationID: "op-a", Attempt: 1,
		Kind: "vm.stop", LeaseToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		LeaseExpiresAt: agentTestNow().Add(time.Minute), Payload: json.RawMessage(`{"vm_id":"vm-a"}`), State: "leased",
	}
}

func TestInternalAgentAPIRequiresTLSAndDoesNotExposePublicV2Route(t *testing.T) {
	repo := &agentRepoStub{}
	handler, ts := newAgentTestServer(t, repo)
	plain := httptest.NewRecorder()
	handler.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/internal/agent/commands", nil))
	if plain.Code != http.StatusUpgradeRequired {
		t.Fatalf("plain HTTP status=%d, want 426", plain.Code)
	}
	resp := doAgentTLS(t, ts, http.MethodGet, "/api/v2/agent/commands", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("public v2 route status=%d, want 404", resp.StatusCode)
	}
}

func TestEnrollmentRequiresHTTPSAndRejectsReplayOrExpiry(t *testing.T) {
	response := protocol.AgentEnrollmentResponseV2{
		ProtocolVersion: protocol.VersionV2, NodeID: "node-enrolled", NodeToken: validAgentV2Command().LeaseToken,
		EnrollmentTarget: "remote_host", ControlPlaneURL: "https://control.example", CredentialGeneration: 1, State: "offline",
	}
	repo := &agentRepoStub{enrollmentResponse: response}
	handler, ts := newAgentTestServer(t, repo)
	plain := httptest.NewRecorder()
	plainRequest := httptest.NewRequest(http.MethodPost, "/internal/agent/enroll", strings.NewReader(`{"protocol_version":"v2","hostname":"agent-01"}`))
	plainRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(plain, plainRequest)
	if plain.Code != http.StatusUpgradeRequired || repo.enrollmentCalls != 0 {
		t.Fatalf("plaintext enrollment status=%d repository calls=%d", plain.Code, repo.enrollmentCalls)
	}

	body := `{"protocol_version":"v2","hostname":"agent-01"}`
	noAuth := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/enroll", body, map[string]string{"Content-Type": "application/json"})
	noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized || repo.enrollmentCalls != 0 {
		t.Fatalf("unauthenticated enrollment status=%d calls=%d", noAuth.StatusCode, repo.enrollmentCalls)
	}
	enrollmentToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/enroll", body, map[string]string{
		"Authorization": "Bearer " + enrollmentToken, "Content-Type": "application/json",
	})
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Pragma") != "no-cache" {
		resp.Body.Close()
		t.Fatalf("enrollment response status=%d cache=%q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var got protocol.AgentEnrollmentResponseV2
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.NodeID != response.NodeID || got.NodeToken != response.NodeToken || repo.enrollmentToken != enrollmentToken ||
		repo.enrollmentRequest.Hostname != "agent-01" || repo.enrollmentCalls != 1 {
		t.Fatalf("enrollment response=%+v repository token=%q request=%+v calls=%d", got, repo.enrollmentToken, repo.enrollmentRequest, repo.enrollmentCalls)
	}

	repo.enrollmentErr = pgstore.ErrEnrollmentTokenUnavailable
	replay := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/enroll", body, map[string]string{
		"Authorization": "Bearer " + enrollmentToken, "Content-Type": "application/json",
	})
	replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed/expired enrollment token status=%d", replay.StatusCode)
	}
}

func TestOperatorEnrollmentTokenIssueIsAuthenticatedNoStoreAndValidated(t *testing.T) {
	repo := &agentRepoStub{enrollmentIssue: v1.AgentEnrollmentTokenIssue{
		ID: "enr_1", Token: "ptenr_once", Target: v1.AgentEnrollmentRemoteHost,
		ControlPlaneURL: "https://control.example", ExpiresAt: agentTestNow().Add(10 * time.Minute),
	}}
	_, ts := newAgentTestServer(t, repo)
	body := `{"target":"remote_host","control_plane_url":"https://control.example"}`
	unauthorized := doAgentTLS(t, ts, http.MethodPost, "/internal/admin/fleet/enrollment-tokens", body, map[string]string{"Content-Type": "application/json"})
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized || repo.enrollmentIssueCalls != 0 {
		t.Fatalf("unauthenticated issue status=%d calls=%d", unauthorized.StatusCode, repo.enrollmentIssueCalls)
	}
	invalid := doAgentTLS(t, ts, http.MethodPost, "/internal/admin/fleet/enrollment-tokens", `{"target":"remote_host","control_plane_url":"http://control.example"}`, map[string]string{
		"Authorization": "Bearer operator-secret", "Content-Type": "application/json",
	})
	invalid.Body.Close()
	if invalid.StatusCode != http.StatusBadRequest || repo.enrollmentIssueCalls != 0 {
		t.Fatalf("invalid issue status=%d calls=%d", invalid.StatusCode, repo.enrollmentIssueCalls)
	}
	issued := doAgentTLS(t, ts, http.MethodPost, "/internal/admin/fleet/enrollment-tokens", body, map[string]string{
		"Authorization": "Bearer operator-secret", "Content-Type": "application/json",
	})
	defer issued.Body.Close()
	if issued.StatusCode != http.StatusCreated || issued.Header.Get("Cache-Control") != "no-store" || issued.Header.Get("Pragma") != "no-cache" || repo.enrollmentIssueCalls != 1 {
		t.Fatalf("token issue status=%d cache=%q calls=%d", issued.StatusCode, issued.Header.Get("Cache-Control"), repo.enrollmentIssueCalls)
	}
	var got v1.AgentEnrollmentTokenIssue
	if err := json.NewDecoder(issued.Body).Decode(&got); err != nil || got.Token != "ptenr_once" {
		t.Fatalf("one-time token response=%+v err=%v", got, err)
	}
}

func TestInternalAgentHeartbeatAndBoundedPolling(t *testing.T) {
	repo := &agentRepoStub{authenticated: true, commands: []protocol.CommandV2{validAgentV2Command()}}
	_, ts := newAgentTestServer(t, repo)
	auth := map[string]string{"Authorization": "Bearer node-token", "X-Porter-Node-ID": "node-a"}
	heartbeat, _ := json.Marshal(protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: agentTestNow()})
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/heartbeat", string(heartbeat), auth)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || repo.heartbeatCalls != 1 {
		t.Fatalf("heartbeat status=%d calls=%d", resp.StatusCode, repo.heartbeatCalls)
	}
	resp = doAgentTLS(t, ts, http.MethodGet, "/internal/agent/commands?limit=7", "", auth)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || repo.requeueCalls != 1 || repo.leaseCalls != 1 || repo.leaseNodeID != "node-a" || repo.leaseLimit != 7 {
		t.Fatalf("poll status=%d requeues=%d leases=%d node=%q limit=%d", resp.StatusCode, repo.requeueCalls, repo.leaseCalls, repo.leaseNodeID, repo.leaseLimit)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("command response cache-control=%q", resp.Header.Get("Cache-Control"))
	}
	var got []protocol.CommandV2
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || len(got) != 1 || got[0].LeaseToken != validAgentV2Command().LeaseToken {
		t.Fatalf("leased commands=%+v err=%v", got, err)
	}
}

func TestInternalAgentResultDuplicateAndStaleFencing(t *testing.T) {
	repo := &agentRepoStub{authenticated: true}
	_, ts := newAgentTestServer(t, repo)
	result := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: "cmd-a", OperationID: "op-a", NodeID: "node-a", Attempt: 2,
		Kind: "vm.stop", LeaseToken: validAgentV2Command().LeaseToken, Success: true, State: "stopped",
	}
	body, _ := json.Marshal(result)
	auth := map[string]string{"Authorization": "Bearer node-token", "X-Porter-Node-ID": "node-a"}
	for range 2 {
		resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/results", string(body), auth)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("duplicate result status=%d", resp.StatusCode)
		}
	}
	if repo.resultCalls != 2 || repo.result != result {
		t.Fatalf("result calls=%d last=%+v", repo.resultCalls, repo.result)
	}
	repo.resultErr = pgstore.ErrCommandAttemptMismatch
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/results", string(body), auth)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || resp.Header.Get("X-Porter-Result-Stale") != "true" {
		t.Fatalf("stale result status=%d marker=%q", resp.StatusCode, resp.Header.Get("X-Porter-Result-Stale"))
	}
}

func TestCredentialProvisioningRequiresAdminAndNoStore(t *testing.T) {
	repo := &agentRepoStub{issue: pgstore.NodeCredentialIssue{NodeID: "node-a", Token: "one-time", Generation: 1}}
	_, ts := newAgentTestServer(t, repo)
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/admin/fleet/nodes/node-a/credentials", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || repo.issueCalls != 0 {
		t.Fatalf("unauthenticated credential provisioning status=%d calls=%d", resp.StatusCode, repo.issueCalls)
	}
	resp = doAgentTLS(t, ts, http.MethodPost, "/internal/admin/fleet/nodes/node-a/credentials", "", map[string]string{"Authorization": "Bearer operator-secret"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Cache-Control") != "no-store" || repo.issueCalls != 1 {
		t.Fatalf("credential provisioning status=%d cache=%q calls=%d", resp.StatusCode, resp.Header.Get("Cache-Control"), repo.issueCalls)
	}
}

func TestCredentialProvisioningAcceptsOnlyEmptyJSONObject(t *testing.T) {
	repo := &agentRepoStub{issue: pgstore.NodeCredentialIssue{NodeID: "node-a", Token: "one-time", Generation: 1}}
	_, ts := newAgentTestServer(t, repo)
	path := "/internal/admin/fleet/nodes/node-a/credentials"
	headers := map[string]string{"Authorization": "Bearer operator-secret", "Content-Type": "application/json"}
	resp := doAgentTLS(t, ts, http.MethodPost, path, `{"unexpected":true}`, headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || repo.issueCalls != 0 {
		t.Fatalf("credential provisioning with fields status=%d calls=%d", resp.StatusCode, repo.issueCalls)
	}
	resp = doAgentTLS(t, ts, http.MethodPost, path, `{}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Cache-Control") != "no-store" || repo.issueCalls != 1 {
		t.Fatalf("empty-object credential provisioning status=%d cache=%q calls=%d", resp.StatusCode, resp.Header.Get("Cache-Control"), repo.issueCalls)
	}
}

func TestAgentAPIStrictlyRejectsUnknownKindsAndBadIdentity(t *testing.T) {
	repo := &agentRepoStub{authenticated: true, commands: []protocol.CommandV2{func() protocol.CommandV2 { c := validAgentV2Command(); c.Kind = "shell.exec"; return c }()}}
	_, ts := newAgentTestServer(t, repo)
	auth := map[string]string{"Authorization": "Bearer node-token", "X-Porter-Node-ID": "node-a"}
	resp := doAgentTLS(t, ts, http.MethodGet, "/internal/agent/commands", "", auth)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unsupported shell command status=%d, want it withheld", resp.StatusCode)
	}

	result := protocol.CommandResultV2{ProtocolVersion: protocol.VersionV2, CommandID: "cmd", OperationID: "op", NodeID: "node-other", Attempt: 1, Kind: "vm.stop", LeaseToken: validAgentV2Command().LeaseToken, Success: true, State: "stopped"}
	body, _ := json.Marshal(result)
	resp = doAgentTLS(t, ts, http.MethodPost, "/internal/agent/results", string(body), auth)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || repo.resultCalls != 0 {
		t.Fatalf("wrong-node result status=%d store calls=%d", resp.StatusCode, repo.resultCalls)
	}
}

func TestAgentAPIReturnsStableRepositoryErrors(t *testing.T) {
	repo := &agentRepoStub{authenticated: true, resultErr: errors.New("database unavailable")}
	_, ts := newAgentTestServer(t, repo)
	result := protocol.CommandResultV2{ProtocolVersion: protocol.VersionV2, CommandID: "cmd", OperationID: "op", NodeID: "node-a", Attempt: 1, Kind: "vm.stop", LeaseToken: validAgentV2Command().LeaseToken, Success: true, State: "stopped"}
	body, _ := json.Marshal(result)
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/results", string(body), map[string]string{"Authorization": "Bearer node-token", "X-Porter-Node-ID": "node-a"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || resp.Header.Get("X-Request-ID") == "" {
		t.Fatalf("repository error status=%d request-id=%q", resp.StatusCode, resp.Header.Get("X-Request-ID"))
	}
}

func TestTelemetryEndpointAuthenticatesAndBindsBatchToNode(t *testing.T) {
	repo := &agentRepoStub{authenticated: true}
	_, ts := newAgentTestServer(t, repo)
	batch := protocol.TelemetryBatchV2{
		ProtocolVersion: protocol.VersionV2,
		NodeID:          "node-a",
		Logs:            []protocol.TelemetryLogV2{{RecordID: "log-1", ReportedAt: agentTestNow(), Level: "info", Message: "agent started"}},
		Metrics:         []protocol.TelemetryMetricV2{{RecordID: "metric-1", ReportedAt: agentTestNow(), Name: "node.cpu.user_ticks", Value: 12, Unit: "ticks"}},
	}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	baseHeaders := map[string]string{"Content-Type": "application/json", "X-Porter-Node-ID": "node-a"}

	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/telemetry", string(body), baseHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || repo.telemetryCalls != 0 {
		t.Fatalf("unauthenticated telemetry status=%d store calls=%d", resp.StatusCode, repo.telemetryCalls)
	}

	batch.NodeID = "node-other"
	body, _ = json.Marshal(batch)
	headers := map[string]string{"Authorization": "Bearer node-token", "Content-Type": "application/json", "X-Porter-Node-ID": "node-a"}
	resp = doAgentTLS(t, ts, http.MethodPost, "/internal/agent/telemetry", string(body), headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || repo.telemetryCalls != 0 {
		t.Fatalf("mismatched batch node status=%d store calls=%d", resp.StatusCode, repo.telemetryCalls)
	}

	batch.NodeID = "node-a"
	body, _ = json.Marshal(batch)
	resp = doAgentTLS(t, ts, http.MethodPost, "/internal/agent/telemetry", string(body), headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || repo.telemetryCalls != 1 || repo.telemetryNodeID != "node-a" {
		t.Fatalf("valid telemetry status=%d calls=%d node=%q", resp.StatusCode, repo.telemetryCalls, repo.telemetryNodeID)
	}
	if len(repo.telemetryBatch.Logs) != 1 || len(repo.telemetryBatch.Metrics) != 1 {
		t.Fatalf("stored batch counts logs=%d metrics=%d", len(repo.telemetryBatch.Logs), len(repo.telemetryBatch.Metrics))
	}
}

func TestTelemetryEndpointRejectsQueryAndUnknownFields(t *testing.T) {
	repo := &agentRepoStub{authenticated: true}
	_, ts := newAgentTestServer(t, repo)
	headers := map[string]string{"Authorization": "Bearer node-token", "Content-Type": "application/json", "X-Porter-Node-ID": "node-a"}
	resp := doAgentTLS(t, ts, http.MethodPost, "/internal/agent/telemetry?node_id=node-a", `{}`, headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("query telemetry status=%d, want 400", resp.StatusCode)
	}
	resp = doAgentTLS(t, ts, http.MethodPost, "/internal/agent/telemetry", `{"protocol_version":"v2","node_id":"node-a","logs":[],"metrics":[],"local_path":"/tmp/x"}`, headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || repo.telemetryCalls != 0 {
		t.Fatalf("unknown field telemetry status=%d calls=%d", resp.StatusCode, repo.telemetryCalls)
	}
}
