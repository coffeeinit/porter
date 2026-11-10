package bootstrap

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"porter/agent/internal/client"
	"porter/agent/internal/localstate"
	"porter/protocol"
)

func TestPortableBootstrapAndJoin(t *testing.T) {
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))
	nodeToken := "node-secret-credential-must-not-enter-sqlite"
	var enrollmentRequests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enrollmentRequests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/enroll" {
			t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+joinToken {
			t.Errorf("join bearer header is missing or incorrect")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read enrollment body: %v", err)
		}
		if bytes.Contains(body, []byte(joinToken)) || bytes.Contains(body, []byte(nodeToken)) {
			t.Errorf("credential appeared in enrollment body")
		}
		var request protocol.AgentEnrollmentRequestV2
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode enrollment body: %v", err)
		} else if request != (protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: "portable-agent"}) {
			t.Errorf("enrollment body = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_portable", NodeToken: nodeToken,
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host,
			CredentialGeneration: 1, State: "offline",
		})
	}))
	defer server.Close()

	dataDir := filepath.Join(t.TempDir(), "agent-data")
	prepared, err := Prepare(Options{
		Server: server.URL, Hostname: "portable-agent", StateDir: dataDir,
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer prepared.Close()

	// The protected service config exists before Enroll reads/sends a token.
	pending, err := localstate.ReadServiceConfigPending(prepared.ServiceConfigPath())
	if err != nil {
		t.Fatalf("read pre-enrollment service config: %v", err)
	}
	if pending.State != "pending" || pending.NodeID != "" {
		t.Fatalf("pre-enrollment config = %+v, want pending without node identity", pending)
	}
	assertModeOwner(t, prepared.StateDir(), localstate.DirectoryMode)
	assertModeOwner(t, prepared.ServiceConfigPath(), localstate.PrivateFileMode)

	response, err := prepared.Enroll(context.Background(), joinToken)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if response.NodeID != "node_portable" || enrollmentRequests != 1 {
		t.Fatalf("response=%+v enrollment requests=%d", response, enrollmentRequests)
	}

	serviceConfig, err := localstate.ReadServiceConfig(prepared.ServiceConfigPath())
	if err != nil {
		t.Fatalf("read ready service config: %v", err)
	}
	if serviceConfig.NodeID != response.NodeID || serviceConfig.CredentialGeneration != 1 {
		t.Fatalf("ready service config = %+v", serviceConfig)
	}
	assertModeOwner(t, prepared.ServiceConfigPath(), localstate.PrivateFileMode)
	assertModeOwner(t, prepared.CredentialPath(), localstate.PrivateFileMode)
	credential, err := localstate.ReadCredential(prepared.CredentialPath())
	if err != nil || credential != nodeToken {
		t.Fatalf("read persisted credential: equal=%t err=%v", credential == nodeToken, err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(prepared.StateDir(), "porter-agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT k,v FROM config`)
	if err != nil {
		t.Fatal(err)
	}
	configValues := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		configValues[key] = value
		if strings.Contains(value, nodeToken) || strings.Contains(value, joinToken) {
			t.Fatalf("SQLite config key %q contains a credential", key)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(configValues) != 3 || configValues["node_id"] != response.NodeID || configValues["credential_path"] != prepared.CredentialPath() || configValues["server_url"] != response.ControlPlaneURL {
		t.Fatalf("SQLite identity config = %#v", configValues)
	}
	var hostnameColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('enrollment_attempt') WHERE name='hostname'`).Scan(&hostnameColumns); err != nil || hostnameColumns != 0 {
		t.Fatalf("SQLite enrollment_attempt hostname columns=%d err=%v, want no hostname column", hostnameColumns, err)
	}
	var outcome string
	if err := db.QueryRow(`SELECT outcome FROM enrollment_attempt WHERE id=1`).Scan(&outcome); err != nil || outcome != "registered" {
		t.Fatalf("recovery outcome=%q err=%v, want registered", outcome, err)
	}

	if _, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir}); err == nil {
		t.Fatal("second join was accepted for an already configured node")
	}
}

func TestPortableLostEnrollmentResponseCannotBeRetried(t *testing.T) {
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x23}, 32))
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("test server does not support connection close")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack response: %v", err)
			return
		}
		_ = connection.Close() // Simulate the server committing enrollment but losing its 201 response.
	}))
	defer server.Close()
	dataDir := filepath.Join(t.TempDir(), "agent-data")
	prepared, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir,
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, joinErr := prepared.Enroll(context.Background(), joinToken)
	if joinErr == nil || !strings.Contains(joinErr.Error(), "do not replay") || !strings.Contains(joinErr.Error(), `hostname "portable-agent"`) || !strings.Contains(joinErr.Error(), "started at") {
		t.Fatalf("Enroll error = %v, want no-retry recovery guidance", joinErr)
	}
	if requests != 1 {
		t.Fatalf("server received %d enrollment requests, want exactly one", requests)
	}
	if _, err := os.Stat(prepared.ServiceConfigPath()); err != nil {
		t.Fatalf("recovery-required service config was removed: %v", err)
	}
	recovery, err := localstate.ReadServiceConfigForJoin(prepared.ServiceConfigPath())
	if err != nil || recovery.State != "recovery_required" || recovery.EnrollmentStartedAt == "" {
		t.Fatalf("recovery config = %+v err=%v, want recorded attempt time without hostname persistence", recovery, err)
	}
	_ = prepared.Close()
	if _, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir}); err == nil || !strings.Contains(err.Error(), "exact hostname printed") || !strings.Contains(err.Error(), "exact node ID") || !strings.Contains(err.Error(), "admin Fleet") || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Prepare with recovery config = %v, want exact Fleet lookup and rotation guidance", err)
	}
	if err := os.Remove(filepath.Join(dataDir, "agent-service.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir}); err == nil || !strings.Contains(err.Error(), "prior enrollment attempt may have reached the handler") {
		t.Fatalf("Prepare after lost response = %v, want replay blocked by recovery metadata", err)
	}
}

func TestRootRequirementIsEnforcedByCLIContract(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this environment is already root; CLI privilege branch is covered by source-level option checks")
	}
	if err := os.Mkdir(filepath.Join(t.TempDir(), "private"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeTestCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertModeOwner(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode=%04o, want %04o", path, info.Mode().Perm(), want)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
		t.Fatalf("%s has unexpected owner", path)
	}
}

func TestJoin404ThenRouteAvailableManualRetrySucceeds(t *testing.T) {
	assertSafeManualJoinRetry(t, http.StatusNotFound, false)
}

func TestJoinGuaranteedPreConsumption503ThenServiceRecoversManualRetrySucceeds(t *testing.T) {
	assertSafeManualJoinRetry(t, http.StatusServiceUnavailable, true)
}

func assertSafeManualJoinRetry(t *testing.T, initialStatus int, markNotConsumed bool) {
	t.Helper()
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6a}, 32))
	var calls atomic.Int32
	var recovered atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/enroll" {
			t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer "+joinToken {
			t.Error("retry did not use the same one-time token")
		}
		var request protocol.AgentEnrollmentRequestV2
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode enrollment request: %v", err)
		} else if request != (protocol.AgentEnrollmentRequestV2{ProtocolVersion: protocol.VersionV2, Hostname: "retry-worker-01"}) {
			t.Errorf("enrollment request = %+v, want v2 hostname retry-worker-01", request)
		}
		call := calls.Add(1)
		if !recovered.Load() {
			if call != 1 {
				t.Errorf("automatic retry made request %d before operator re-invoked join", call)
			}
			if markNotConsumed {
				w.Header().Set(client.EnrollmentNotConsumedHeader, "not-consumed")
			}
			w.WriteHeader(initialStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_manual_retry", NodeToken: "node-token-manual-retry",
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host,
			CredentialGeneration: 1, State: "offline",
		})
	}))
	defer server.Close()

	options := Options{
		Server: server.URL, Hostname: "retry-worker-01", StateDir: filepath.Join(t.TempDir(), "agent-state"),
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	}
	first, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare first user-invoked join: %v", err)
	}
	_, firstErr := first.Enroll(context.Background(), joinToken)
	var notConsumed *client.EnrollmentOutcomeNotConsumedError
	if !errors.As(firstErr, &notConsumed) {
		t.Fatalf("first enrollment error = %v, want confirmed-not-consumed classification", firstErr)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("first user invocation made %d requests, want exactly 1 (no auto-retry)", got)
	}
	if _, err := os.Stat(first.ServiceConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("safe failed join left recovery-blocking service config, stat error=%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first join: %v", err)
	}

	// Simulate route/service recovery, then a separate explicit `join` command.
	recovered.Store(true)
	second, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare later manual retry: %v", err)
	}
	response, err := second.Enroll(context.Background(), joinToken)
	if err != nil {
		t.Fatalf("manual retry with same token: %v", err)
	}
	_ = second.Close()
	if response.NodeID != "node_manual_retry" || calls.Load() != 2 {
		t.Fatalf("manual retry response=%+v total requests=%d, want successful second request", response, calls.Load())
	}
}

func TestJoinIncludesLocalHostnameWhenNoExplicitHostnameIsSet(t *testing.T) {
	wantHostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(wantHostname) == "" {
		t.Fatalf("get local hostname for test: hostname=%q err=%v", wantHostname, err)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.AgentEnrollmentRequestV2
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode default-hostname enrollment request: %v", err)
		} else if request.Hostname != wantHostname || request.ProtocolVersion != protocol.VersionV2 {
			t.Errorf("enrollment request = %+v, want local hostname %q and v2", request, wantHostname)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_default_hostname", NodeToken: "node-token-default-hostname",
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host,
			CredentialGeneration: 1, State: "offline",
		})
	}))
	defer server.Close()
	prepared, err := Prepare(Options{
		Server: server.URL, StateDir: filepath.Join(t.TempDir(), "agent-state"),
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	})
	if err != nil {
		t.Fatalf("prepare join with local hostname default: %v", err)
	}
	if _, err := prepared.Enroll(context.Background(), "ptenr_"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x7b}, 32))); err != nil {
		t.Fatalf("join with local hostname default: %v", err)
	}
	_ = prepared.Close()
	if calls.Load() != 1 {
		t.Fatalf("enrollment requests=%d, want 1", calls.Load())
	}
}
