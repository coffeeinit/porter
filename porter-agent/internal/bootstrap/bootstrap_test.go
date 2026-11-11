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
	"time"

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
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('enrollment_attempt') WHERE name='hostname'`).Scan(&hostnameColumns); err != nil || hostnameColumns != 1 {
		t.Fatalf("SQLite enrollment_attempt hostname columns=%d err=%v, want one recovery-metadata column", hostnameColumns, err)
	}
	var storedHostname string
	if err := db.QueryRow(`SELECT hostname FROM enrollment_attempt WHERE id=1`).Scan(&storedHostname); err != nil || storedHostname != "portable-agent" {
		t.Fatalf("SQLite recovery hostname=%q err=%v, want portable-agent", storedHostname, err)
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
	_, outcome, found, stateErr := prepared.store.JoinAttemptOutcome()
	if stateErr != nil || !found || outcome != "outcome_unknown" {
		t.Fatalf("lost-201 recovery outcome = (%q, %t, %v), want persisted outcome_unknown", outcome, found, stateErr)
	}
	attempt, found, stateErr := prepared.store.JoinAttemptRecovery()
	if stateErr != nil || !found || attempt.Hostname != "portable-agent" || attempt.StartedAt.IsZero() || attempt.Outcome != "outcome_unknown" {
		t.Fatalf("lost-201 recovery metadata = (%+v, %t, %v), want hostname/time/outcome persisted", attempt, found, stateErr)
	}
	if _, err := os.Stat(prepared.ServiceConfigPath()); err != nil {
		t.Fatalf("recovery-required service config was removed: %v", err)
	}
	recovery, err := localstate.ReadServiceConfigForJoin(prepared.ServiceConfigPath())
	if err != nil || recovery.State != "recovery_required" || recovery.EnrollmentStartedAt == "" {
		t.Fatalf("recovery config = %+v err=%v, want recorded attempt time without hostname persistence", recovery, err)
	}
	_ = prepared.Close()
	if _, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir}); err == nil || !strings.Contains(err.Error(), "exact hostname printed") || !strings.Contains(err.Error(), "portable-agent") || !strings.Contains(err.Error(), "exact node ID") || !strings.Contains(err.Error(), "admin Fleet") || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Prepare with recovery config = %v, want exact Fleet lookup and rotation guidance", err)
	}
	if err := os.Remove(filepath.Join(dataDir, "agent-service.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Server: server.URL, Hostname: "portable-agent", StateDir: dataDir}); err == nil || !strings.Contains(err.Error(), `prior enrollment attempt has outcome "outcome_unknown"`) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("Prepare after lost response = %v, want replay blocked by recovery metadata", err)
	}
}

func TestPortableTimeoutPersistsUnknownAndCannotBeRetried(t *testing.T) {
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32))
	requestStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{}, 1)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		<-releaseHandler
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	defer func() {
		select {
		case releaseHandler <- struct{}{}:
		default:
		}
	}()
	dataDir := filepath.Join(t.TempDir(), "agent-data")
	prepared, err := Prepare(Options{Server: server.URL, Hostname: "timeout-worker", StateDir: dataDir,
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	joinResult := make(chan error, 1)
	go func() {
		_, err := prepared.Enroll(ctx, joinToken)
		joinResult <- err
	}()
	select {
	case <-requestStarted:
	case err := <-joinResult:
		t.Fatalf("join returned before server received request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive enrollment request")
	}
	joinErr := <-joinResult
	releaseHandler <- struct{}{}
	var unknown *client.EnrollmentOutcomeUnknownError
	if !errors.As(joinErr, &unknown) || !strings.Contains(joinErr.Error(), "do not replay") {
		t.Fatalf("timed-out join error = %v, want persisted unknown/no-replay classification", joinErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want exactly one", got)
	}
	_, outcome, found, err := prepared.store.JoinAttemptOutcome()
	if err != nil || !found || outcome != "outcome_unknown" {
		t.Fatalf("timeout recovery outcome = (%q, %t, %v), want persisted outcome_unknown", outcome, found, err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("close recovery-required join: %v", err)
	}
	if _, err := Prepare(Options{Server: server.URL, Hostname: "timeout-worker", StateDir: dataDir}); err == nil || !strings.Contains(err.Error(), "exact hostname") {
		t.Fatalf("Prepare after timeout = %v, want replay blocked with Fleet recovery guidance", err)
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

func TestJoinEveryNon201ResponsePersistsUnknownAndBlocksReplay(t *testing.T) {
	statuses := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusGone,
		http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
	}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(status)}, 32))
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/enroll" {
					t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", r.Method, r.URL.String())
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "ambiguous response")
			}))
			defer server.Close()

			options := Options{
				Server: server.URL, Hostname: "recovery-worker-01", StateDir: filepath.Join(t.TempDir(), "agent-state"),
				TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
			}
			first, err := Prepare(options)
			if err != nil {
				t.Fatalf("prepare join: %v", err)
			}
			_, joinErr := first.Enroll(context.Background(), joinToken)
			var unknown *client.EnrollmentOutcomeUnknownError
			if !errors.As(joinErr, &unknown) || !strings.Contains(joinErr.Error(), "do not replay") {
				t.Fatalf("HTTP %d join error = %v, want outcome_unknown/no-replay", status, joinErr)
			}
			storedServer, outcome, found, err := first.store.JoinAttemptOutcome()
			if err != nil || !found || storedServer != server.URL || outcome != "outcome_unknown" {
				t.Fatalf("stored enrollment attempt = (%q, %q, %t, %v), want (%q, outcome_unknown, true)", storedServer, outcome, found, err, server.URL)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("HTTP %d request count = %d, want exactly one", status, got)
			}
			if err := first.Close(); err != nil {
				t.Fatalf("close recovery-required join: %v", err)
			}
			if _, err := Prepare(options); err == nil || !strings.Contains(err.Error(), "exact hostname") || !strings.Contains(err.Error(), "exact node ID") || !strings.Contains(err.Error(), "rotate credentials through admin Fleet") || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("new join after HTTP %d = %v, want Fleet lookup/rotation guidance and replay block", status, err)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("recovery attempted another HTTP %d request; total=%d", status, got)
			}
		})
	}
}

func TestMalformed201PersistsUnknownAndBlocksReplay(t *testing.T) {
	joinToken := testJoinTokenForBootstrap()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_malformed", NodeToken: strings.Repeat("n", 43),
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host,
			CredentialGeneration: 1, State: "offline",
		})
		_, _ = io.WriteString(w, `{"second":"json value"}`)
	}))
	defer server.Close()
	options := Options{
		Server: server.URL, Hostname: "malformed-201-worker", StateDir: filepath.Join(t.TempDir(), "agent-state"),
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	}
	prepared, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare join: %v", err)
	}
	_, joinErr := prepared.Enroll(context.Background(), joinToken)
	var unknown *client.EnrollmentOutcomeUnknownError
	if !errors.As(joinErr, &unknown) || !strings.Contains(joinErr.Error(), "do not replay") {
		t.Fatalf("malformed 201 join error = %v, want outcome_unknown/no-replay", joinErr)
	}
	attempt, found, err := prepared.store.JoinAttemptRecovery()
	if err != nil || !found || attempt.Outcome != "outcome_unknown" || attempt.Hostname != options.Hostname || attempt.StartedAt.IsZero() {
		t.Fatalf("malformed 201 recovery record = (%+v, %t, %v), want persisted unknown outcome/hostname/time", attempt, found, err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("close recovery-required join: %v", err)
	}
	if _, err := Prepare(options); err == nil || !strings.Contains(err.Error(), "malformed-201-worker") || !strings.Contains(err.Error(), "outcome_unknown") || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("restart after malformed 201 = %v, want persisted replay block with hostname", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("malformed 201 caused %d enrollment requests after restart, want exactly one", got)
	}
}

func TestJoin503AfterRequestBeginsPersistsUnknownAndBlocksReplay(t *testing.T) {
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			t.Errorf("read begun enrollment request body: bytes=%d err=%v", len(body), err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/enroll" {
			t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "injected bare 503")
	}))
	defer server.Close()
	options := Options{
		Server: server.URL, Hostname: "503-recovery-worker", StateDir: filepath.Join(t.TempDir(), "agent-state"),
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	}
	prepared, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare join: %v", err)
	}
	_, joinErr := prepared.Enroll(context.Background(), joinToken)
	var unknown *client.EnrollmentOutcomeUnknownError
	if !errors.As(joinErr, &unknown) || !strings.Contains(joinErr.Error(), "do not replay") {
		t.Fatalf("injected 503 join error = %v, want outcome_unknown/no-replay", joinErr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want exactly one", got)
	}
	_, outcome, found, err := prepared.store.JoinAttemptOutcome()
	if err != nil || !found || outcome != "outcome_unknown" {
		t.Fatalf("injected 503 stored outcome = (%q, %t, %v), want outcome_unknown", outcome, found, err)
	}
	serviceConfigPath := prepared.ServiceConfigPath()
	if err := prepared.Close(); err != nil {
		t.Fatalf("close recovery-required join: %v", err)
	}
	if _, err := Prepare(options); err == nil || !strings.Contains(err.Error(), "exact hostname") || !strings.Contains(err.Error(), "exact node ID") {
		t.Fatalf("restart after injected 503 = %v, want blocked with Fleet recovery guidance", err)
	}
	if err := os.Remove(serviceConfigPath); err != nil {
		t.Fatalf("remove recovery file to verify SQLite replay fence: %v", err)
	}
	if _, err := Prepare(options); err == nil || !strings.Contains(err.Error(), `prior enrollment attempt has outcome "outcome_unknown"`) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("restart after recovery-file removal = %v, want SQLite attempt fence", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("restart/recovery sent another enrollment request: total=%d, want one", got)
	}
}

func TestCrashAfterPendingConfigAllowsSafeRestart(t *testing.T) {
	options := Options{Server: "https://control.example", Hostname: "pending-crash-worker", StateDir: filepath.Join(t.TempDir(), "agent-state")}
	crashErr := errors.New("injected crash after pending config")
	_, err := prepareWithCrashInjector(options, func(point crashPoint) error {
		if point == crashAfterPendingConfig {
			return crashErr
		}
		return nil
	})
	if !errors.Is(err, crashErr) {
		t.Fatalf("prepare with pending-config crash = %v, want injected crash", err)
	}
	configPath := filepath.Join(options.StateDir, "agent-service.json")
	if config, err := localstate.ReadServiceConfigPending(configPath); err != nil || config.State != "pending" {
		t.Fatalf("persisted pre-send config = %+v, err=%v; want pending", config, err)
	}

	restarted, err := Prepare(options)
	if err != nil {
		t.Fatalf("restart after pending-config crash: %v", err)
	}
	defer restarted.Close()
	if config, err := localstate.ReadServiceConfigPending(configPath); err != nil || config.State != "pending" {
		t.Fatalf("restarted config = %+v, err=%v; want fresh pending config", config, err)
	}
	_, outcome, found, err := restarted.store.JoinAttemptOutcome()
	if err != nil || found {
		t.Fatalf("join attempt after pre-send crash = (%q, %t, %v), want no recorded request", outcome, found, err)
	}
}

func TestCrashAfterRecoveryConfigBeforeSendAllowsSafeRestart(t *testing.T) {
	options := Options{Server: "https://control.example", Hostname: "recovery-config-crash-worker", StateDir: filepath.Join(t.TempDir(), "agent-state")}
	prepared, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare join: %v", err)
	}
	crashErr := errors.New("injected crash after recovery config")
	prepared.crashInjector = func(point crashPoint) error {
		if point == crashAfterRecoveryConfig {
			return crashErr
		}
		return nil
	}
	_, err = prepared.Enroll(context.Background(), testJoinTokenForBootstrap())
	if !errors.Is(err, crashErr) {
		t.Fatalf("enroll with recovery-config crash = %v, want injected crash", err)
	}
	if config, err := localstate.ReadServiceConfigForJoin(prepared.ServiceConfigPath()); err != nil || config.State != "recovery_required" {
		t.Fatalf("persisted recovery config = %+v, err=%v", config, err)
	}
	if _, outcome, found, err := prepared.store.JoinAttemptOutcome(); err != nil || found {
		t.Fatalf("join attempt at pre-send crash = (%q, %t, %v), want none", outcome, found, err)
	}
	closeWithoutJoinCleanup(t, prepared)

	restarted, err := Prepare(options)
	if err != nil {
		t.Fatalf("restart after recovery-config crash: %v", err)
	}
	defer restarted.Close()
	if config, err := localstate.ReadServiceConfigPending(restarted.ServiceConfigPath()); err != nil || config.State != "pending" {
		t.Fatalf("restart config = %+v, err=%v; want pending", config, err)
	}
}

func TestCrashAfterRejectedOutcomeIsReconciledOnRestart(t *testing.T) {
	options := Options{Server: "https://control.example", Hostname: "rejected-crash-worker", StateDir: filepath.Join(t.TempDir(), "agent-state")}
	prepared, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare join: %v", err)
	}
	prepared.options.Hostname = strings.Repeat("x", 129)
	crashErr := errors.New("injected crash after rejected outcome")
	prepared.crashInjector = func(point crashPoint) error {
		if point == crashAfterRejectedOutcome {
			return crashErr
		}
		return nil
	}
	_, err = prepared.Enroll(context.Background(), testJoinTokenForBootstrap())
	if !errors.Is(err, crashErr) {
		t.Fatalf("enroll with rejected-outcome crash = %v, want injected crash", err)
	}
	_, outcome, found, err := prepared.store.JoinAttemptOutcome()
	if err != nil || !found || outcome != "rejected" {
		t.Fatalf("SQLite join outcome before injected crash = (%q, %t, %v), want rejected", outcome, found, err)
	}
	if config, err := localstate.ReadServiceConfigForJoin(prepared.ServiceConfigPath()); err != nil || config.State != "recovery_required" {
		t.Fatalf("config before injected crash = %+v, err=%v; want recovery_required", config, err)
	}
	closeWithoutJoinCleanup(t, prepared)

	restarted, err := Prepare(options)
	if err != nil {
		t.Fatalf("restart after rejected-outcome crash: %v", err)
	}
	defer restarted.Close()
	if config, err := localstate.ReadServiceConfigPending(restarted.ServiceConfigPath()); err != nil || config.State != "pending" {
		t.Fatalf("reconciled config = %+v, err=%v; want pending", config, err)
	}
	_, outcome, found, err = restarted.store.JoinAttemptOutcome()
	if err != nil || !found || outcome != "rejected" {
		t.Fatalf("persisted rejected outcome after restart = (%q, %t, %v)", outcome, found, err)
	}
}

func TestJoinLocalPreSendRejectionIsPersistedAndCanBeCorrected(t *testing.T) {
	joinToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6a}, 32))
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request protocol.AgentEnrollmentRequestV2
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode corrected enrollment request: %v", err)
		} else if request.Hostname != "local-worker" {
			t.Errorf("corrected enrollment hostname = %q, want local-worker", request.Hostname)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_corrected", NodeToken: "node-token-corrected",
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host, CredentialGeneration: 1, State: "offline",
		})
	}))
	defer server.Close()
	options := Options{Server: server.URL, Hostname: "local-worker", StateDir: filepath.Join(t.TempDir(), "agent-state"), TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)}}
	prepared, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare join: %v", err)
	}
	prepared.options.Hostname = strings.Repeat("x", 129)
	_, joinErr := prepared.Enroll(context.Background(), joinToken)
	var rejected *client.EnrollmentOutcomeRejectedError
	if !errors.As(joinErr, &rejected) {
		t.Fatalf("local join error = %v, want rejected before transmission", joinErr)
	}
	if calls.Load() != 0 {
		t.Fatalf("local validation sent %d requests, want none", calls.Load())
	}
	_, outcome, found, err := prepared.store.JoinAttemptOutcome()
	if err != nil || !found || outcome != "rejected" {
		t.Fatalf("stored local outcome = (%q, %t, %v), want rejected", outcome, found, err)
	}
	if _, err := os.Stat(prepared.ServiceConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("pre-send rejected join left pending config, stat error=%v", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("close rejected join: %v", err)
	}
	prepared, err = Prepare(options)
	if err != nil {
		t.Fatalf("prepare corrected local join: %v", err)
	}
	correctedToken := "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6b}, 32))
	response, err := prepared.Enroll(context.Background(), correctedToken)
	if err != nil || response.NodeID != "node_corrected" {
		t.Fatalf("corrected join response = (%+v, %v), want successful fresh-token enrollment", response, err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("close corrected join: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("corrected join sent %d requests, want exactly one after the pre-send rejection", calls.Load())
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

func testJoinTokenForBootstrap() string {
	return "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6d}, 32))
}

// closeWithoutJoinCleanup models process death: release OS resources without
// running Prepared.Close, which would otherwise perform graceful cancellation.
func closeWithoutJoinCleanup(t *testing.T, prepared *Prepared) {
	t.Helper()
	if prepared.store != nil {
		if err := prepared.store.Close(); err != nil {
			t.Errorf("close SQLite store after simulated crash: %v", err)
		}
		prepared.store = nil
	}
	if prepared.joinLock != nil {
		if err := prepared.joinLock.Close(); err != nil {
			t.Errorf("close join lock after simulated crash: %v", err)
		}
		prepared.joinLock = nil
	}
}
