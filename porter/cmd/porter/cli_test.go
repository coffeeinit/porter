package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
)

const cliTestOperatorToken = "operator-token-for-cli-contract-test"

type cliObservedRequest struct {
	method        string
	path          string
	query         string
	authorization string
	contentType   string
	tls           bool
	body          []byte
}

type cliContractBackend struct {
	mu       sync.Mutex
	requests []cliObservedRequest
}

func (b *cliContractBackend) record(r *http.Request, body []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, cliObservedRequest{
		method:        r.Method,
		path:          r.URL.Path,
		query:         r.URL.RawQuery,
		authorization: r.Header.Get("Authorization"),
		contentType:   r.Header.Get("Content-Type"),
		tls:           r.TLS != nil,
		body:          append([]byte(nil), body...),
	})
}

func (b *cliContractBackend) snapshotRequests() []cliObservedRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]cliObservedRequest(nil), b.requests...)
}

func (b *cliContractBackend) checkOperatorRequest(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.TLS == nil {
		writeCLIContractError(w, http.StatusUpgradeRequired, "HTTPS required")
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeCLIContractError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if r.URL.RawQuery != "" {
		writeCLIContractError(w, http.StatusBadRequest, "query parameters are not accepted")
		return false
	}
	if r.Header.Get("Authorization") != "Bearer "+cliTestOperatorToken {
		writeCLIContractError(w, http.StatusUnauthorized, "operator bearer token required")
		return false
	}
	return true
}

func (b *cliContractBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.record(r, body)
	if r.URL.Path != "/internal/admin/fleet/enrollment-tokens" {
		writeCLIContractError(w, http.StatusNotFound, "unexpected backend route")
		return
	}
	if !b.checkOperatorRequest(w, r, http.MethodPost) {
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 2 {
		writeCLIContractError(w, http.StatusBadRequest, "invalid join-token DTO")
		return
	}
	var request v1.CreateAgentEnrollmentTokenRequest
	if err := json.Unmarshal(body, &request); err != nil ||
		request.Target != v1.AgentEnrollmentTarget("remote_host") ||
		request.ControlPlaneURL != "https://porter.example.com" {
		writeCLIContractError(w, http.StatusBadRequest, "join-token DTO does not match the PostgreSQL Fleet contract")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(v1.AgentEnrollmentTokenIssue{
		ID:              "enr_cli_test",
		Token:           "ptenr_one_time_cli_test_secret",
		Target:          request.Target,
		ControlPlaneURL: request.ControlPlaneURL,
		ExpiresAt:       time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC),
	})
}

func (b *cliContractBackend) FleetCapabilitiesHandler(capabilities v1.FleetCapabilities) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.record(r, body)
		if !b.checkOperatorRequest(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(capabilities)
	})
}

func writeCLIContractError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    "unauthenticated",
			"message": message,
		},
	})
}

func TestRunCLIUsesPostgresFleetContract(t *testing.T) {
	backend := &cliContractBackend{}
	controlPlane := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		backend.record(r, body)
		if r.TLS == nil {
			writeCLIContractError(w, http.StatusUpgradeRequired, "HTTPS required")
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/fleet/nodes" || r.URL.RawQuery != "" {
			writeCLIContractError(w, http.StatusNotFound, "unexpected Fleet nodes request")
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+cliTestOperatorToken {
			writeCLIContractError(w, http.StatusUnauthorized, "operator bearer token required")
			return
		}
		stamp := time.Date(2026, 11, 11, 12, 30, 0, 0, time.UTC)
		lastSeen := stamp.Add(-time.Minute)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v1.Page[v1.FleetNode]{Items: []v1.FleetNode{{
			ID:               "node_cli_test",
			Name:             "worker-01",
			State:            v1.FleetNodeState("ready"),
			EnrollmentTarget: v1.AgentEnrollmentTarget("remote_host"),
			LastSeenAt:       &lastSeen,
			CreatedAt:        stamp,
			UpdatedAt:        stamp,
		}}})
	})
	mux := postgresOnlyMux(controlPlane, backend)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	t.Setenv("PORTER_URL", server.URL)
	t.Setenv("PORTER_TOKEN", cliTestOperatorToken)

	status, stdout, stderr := runCLIWithOutput(t, "status")
	if status != 0 || stderr != "" {
		t.Fatalf("status command code=%d stderr=%q", status, stderr)
	}
	var capabilities map[string]bool
	if err := json.Unmarshal([]byte(stdout), &capabilities); err != nil {
		t.Fatalf("decode capability DTO: %v; output=%q", err, stdout)
	}
	if len(capabilities) != 3 || !capabilities["join_token_issuance"] || !capabilities["agent_enrollment"] || !capabilities["node_credential_rotation"] {
		t.Fatalf("unexpected exact Fleet capabilities DTO: %#v", capabilities)
	}

	status, stdout, stderr = runCLIWithOutput(t, "nodes")
	if status != 0 || stderr != "" {
		t.Fatalf("nodes command code=%d stderr=%q", status, stderr)
	}
	var nodePage map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &nodePage); err != nil {
		t.Fatalf("decode Fleet node page: %v; output=%q", err, stdout)
	}
	if len(nodePage) != 1 || nodePage["items"] == nil {
		t.Fatalf("Fleet nodes response is not the expected Page[FleetNode] DTO: %#v", nodePage)
	}
	var nodes []map[string]json.RawMessage
	if err := json.Unmarshal(nodePage["items"], &nodes); err != nil || len(nodes) != 1 {
		t.Fatalf("decode Fleet node items: %v; items=%s", err, nodePage["items"])
	}
	if len(nodes[0]) != 7 {
		t.Fatalf("FleetNode DTO field count=%d, want 7: %#v", len(nodes[0]), nodes[0])
	}
	for _, field := range []string{"id", "name", "state", "enrollment_target", "last_seen_at", "created_at", "updated_at"} {
		if nodes[0][field] == nil {
			t.Errorf("FleetNode DTO is missing %q", field)
		}
	}

	status, stdout, stderr = runCLIWithOutput(t, "join-token", "remote_host", "https://porter.example.com")
	if status != 0 || stderr != "" {
		t.Fatalf("join-token command code=%d stderr=%q", status, stderr)
	}
	var issued map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &issued); err != nil {
		t.Fatalf("decode join-token issue DTO: %v; output=%q", err, stdout)
	}
	if len(issued) != 5 {
		t.Fatalf("join-token DTO field count=%d, want 5: %#v", len(issued), issued)
	}
	for _, field := range []string{"id", "token", "target", "control_plane_url", "expires_at"} {
		if issued[field] == nil {
			t.Errorf("join-token issue DTO is missing %q", field)
		}
	}

	requests := backend.snapshotRequests()
	if len(requests) != 3 {
		t.Fatalf("recorded %d backend requests, want 3: %#v", len(requests), requests)
	}
	want := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/fleet/capabilities"},
		{http.MethodGet, "/api/v1/fleet/nodes"},
		{http.MethodPost, "/internal/admin/fleet/enrollment-tokens"},
	}
	for i, request := range requests {
		if request.method != want[i].method || request.path != want[i].path {
			t.Errorf("request %d = %s %s, want %s %s", i, request.method, request.path, want[i].method, want[i].path)
		}
		if request.authorization != "Bearer "+cliTestOperatorToken {
			t.Errorf("request %d did not use the operator Bearer token", i)
		}
		if !request.tls {
			t.Errorf("request %d did not use HTTPS", i)
		}
		if request.query != "" {
			t.Errorf("request %d unexpectedly included query parameters %q", i, request.query)
		}
	}
	wantIssueBody, err := json.Marshal(v1.CreateAgentEnrollmentTokenRequest{
		Target:          v1.AgentEnrollmentTarget("remote_host"),
		ControlPlaneURL: "https://porter.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requests[2].body, wantIssueBody) {
		t.Errorf("join-token request body=%s, want exact DTO %s", requests[2].body, wantIssueBody)
	}
	if requests[2].contentType != "application/json" {
		t.Errorf("join-token Content-Type=%q, want application/json", requests[2].contentType)
	}

	t.Setenv("PORTER_TOKEN", "wrong-operator-token")
	status, _, stderr = runCLIWithOutput(t, "status")
	if status != 1 || !strings.Contains(stderr, "401 Unauthorized") || strings.Contains(stderr, "operator-token-for-cli-contract-test") {
		t.Fatalf("invalid operator auth was not safely rejected: code=%d stderr=%q", status, stderr)
	}
}

func TestRunCLIRetiresLegacyCommandsWithoutNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	t.Setenv("PORTER_URL", server.URL)
	t.Setenv("PORTER_TOKEN", "")

	for _, command := range []string{"projects", "deploy", "logs", "scale", "rollback", "backup"} {
		status, _, stderr := runCLIWithOutput(t, command)
		if status != 2 || !strings.Contains(stderr, "not supported") || !strings.Contains(stderr, "no request was sent") {
			t.Errorf("retired command %q: code=%d stderr=%q", command, status, stderr)
		}
	}
	if requests != 0 {
		t.Fatalf("retired commands sent %d network requests", requests)
	}
}

func TestRunCLIValidatesJoinTokenArgumentsBeforeNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	t.Setenv("PORTER_URL", server.URL)
	t.Setenv("PORTER_TOKEN", cliTestOperatorToken)

	for _, args := range [][]string{
		{"join-token"},
		{"join-token", "invalid", "https://porter.example.com"},
		{"join-token", "remote_host", "http://porter.example.com"},
		{"join-token", "remote_host", "https://porter.example.com/path"},
	} {
		status, _, stderr := runCLIWithOutput(t, args...)
		if status != 2 || stderr == "" {
			t.Errorf("invalid join-token args %q: code=%d stderr=%q", args, status, stderr)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid join-token arguments sent %d network requests", requests)
	}
}

func runCLIWithOutput(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		stdoutReader.Close()
		stdoutWriter.Close()
		t.Fatal(err)
	}
	previousStdout, previousStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	status := runCLI(args)
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	os.Stdout, os.Stderr = previousStdout, previousStderr
	stdout, stdoutErr := io.ReadAll(stdoutReader)
	stderr, stderrErr := io.ReadAll(stderrReader)
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
	if stdoutErr != nil || stderrErr != nil {
		t.Fatalf("read captured CLI output: stdout=%v stderr=%v", stdoutErr, stderrErr)
	}
	return status, string(stdout), string(stderr)
}
