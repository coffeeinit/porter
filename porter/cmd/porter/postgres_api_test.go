package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/systemdcred"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresOnlyMuxRoutesAndNeverFallsBack(t *testing.T) {
	controlPlaneCalls, agentCalls := 0, 0
	var agentPaths []string
	controlPlane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		controlPlaneCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	agents := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentCalls++
		agentPaths = append(agentPaths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := postgresOnlyMux(controlPlane, agents)

	for _, tc := range []struct {
		path       string
		wantStatus int
		wantCP     int
		wantAgent  int
		wantPath   string
	}{
		{path: "/api/v1/fleet/nodes", wantStatus: http.StatusNoContent, wantCP: 1},
		{path: "/api/v1/fleet/join-tokens", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 1, wantPath: "/internal/admin/fleet/enrollment-tokens"},
		{path: "/api/v1/fleet/enroll", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 2, wantPath: "/internal/agent/enroll"},
		{path: "/api/v1/fleet/nodes/node-a/credentials", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 3, wantPath: "/internal/admin/fleet/nodes/node-a/credentials"},
		{path: "/internal/agent/heartbeat", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 4, wantPath: "/internal/agent/heartbeat"},
		{path: "/internal/agent/telemetry", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 5, wantPath: "/internal/agent/telemetry"},
		{path: "/internal/agent/commands", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 6, wantPath: "/internal/agent/commands"},
		{path: "/internal/agent/results", wantStatus: http.StatusNoContent, wantCP: 1, wantAgent: 7, wantPath: "/internal/agent/results"},
		{path: "/api/v1/fleet/capabilities", wantStatus: http.StatusNotFound, wantCP: 1, wantAgent: 7},
		{path: "/internal/agent/enroll", wantStatus: http.StatusGone, wantCP: 1, wantAgent: 7},
		{path: "/internal/admin/fleet/enrollment-tokens", wantStatus: http.StatusNotFound, wantCP: 1, wantAgent: 7},
		{path: "/v1/fleet/nodes", wantStatus: http.StatusGone, wantCP: 1, wantAgent: 7},
		{path: "/v1/fleet/join-tokens", wantStatus: http.StatusGone, wantCP: 1, wantAgent: 7},
		{path: "/v1/projects", wantStatus: http.StatusGone, wantCP: 1, wantAgent: 7},
		{path: "/v1", wantStatus: http.StatusGone, wantCP: 1, wantAgent: 7},
		{path: "/", wantStatus: http.StatusServiceUnavailable, wantCP: 1, wantAgent: 7},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if response.Code != tc.wantStatus || controlPlaneCalls != tc.wantCP || agentCalls != tc.wantAgent {
			t.Fatalf("%s: status=%d controlPlaneCalls=%d agentCalls=%d, want status=%d controlPlaneCalls=%d agentCalls=%d", tc.path, response.Code, controlPlaneCalls, agentCalls, tc.wantStatus, tc.wantCP, tc.wantAgent)
		}
		if tc.wantPath != "" && agentPaths[len(agentPaths)-1] != tc.wantPath {
			t.Fatalf("%s was routed to %q, want %q", tc.path, agentPaths[len(agentPaths)-1], tc.wantPath)
		}
		if tc.wantStatus == http.StatusGone {
			var envelope v1.ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.RequestID == "" || response.Header().Get("X-Request-ID") != envelope.Error.RequestID {
				t.Fatalf("retired route error envelope=%+v err=%v", envelope.Error, err)
			}
		}
		if tc.path == "/" && (!strings.Contains(response.Body.String(), "not verified as PostgreSQL-only") || strings.Contains(response.Body.String(), "Demo preview")) {
			t.Fatal("unsafe embedded panel was not clearly withheld")
		}
	}
}

func TestPostgresAPIRouteConfigurationFailsClosed(t *testing.T) {
	if handler, closeDB, err := configureAPIRoutes("operator-token", ""); err == nil || handler != nil || closeDB != nil || !strings.Contains(err.Error(), "PostgreSQL DSN credential") {
		t.Fatal("missing PostgreSQL credential did not fail closed")
	}
	if handler, closeDB, err := configureAPIRoutes("", "postgres://porter:unused@127.0.0.1:1/porter"); err == nil || handler != nil || closeDB != nil || !strings.Contains(err.Error(), "operator admin credential") {
		t.Fatal("missing operator credential did not fail closed")
	}
}

func TestPostgresConnectionFailureDoesNotLeakDSN(t *testing.T) {
	dsn := "postgresql://porter:do-not-print@127.0.0.1:1/porter?sslmode=disable"
	if _, closeDB, err := configureAPIRoutes("operator-secret", dsn); err == nil {
		closeDB()
		t.Fatal("invalid database endpoint unexpectedly connected")
	} else if strings.Contains(err.Error(), dsn) || strings.Contains(err.Error(), "do-not-print") {
		t.Fatal("PostgreSQL connection failure exposed DSN data")
	}
}

func TestFleetCapabilitiesReflectOnlyMountedRoutes(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cases := []struct {
		name   string
		routes fleetPublicRoutes
		want   v1.FleetCapabilities
	}{
		{
			name:   "all",
			routes: fleetPublicRoutes{joinTokenIssue: ok, agentEnrollment: ok, nodeList: ok, nodeCredentialRotate: ok},
			want:   v1.FleetCapabilities{JoinTokenIssuance: true, AgentEnrollment: true, NodeCredentialRotation: true},
		},
		{
			name:   "missing issue route",
			routes: fleetPublicRoutes{agentEnrollment: ok, nodeList: ok, nodeCredentialRotate: ok},
			want:   v1.FleetCapabilities{AgentEnrollment: true, NodeCredentialRotation: true},
		},
		{
			name:   "missing enrollment route",
			routes: fleetPublicRoutes{joinTokenIssue: ok, nodeList: ok, nodeCredentialRotate: ok},
			want:   v1.FleetCapabilities{JoinTokenIssuance: true, NodeCredentialRotation: true},
		},
		{
			name:   "missing rotation route",
			routes: fleetPublicRoutes{joinTokenIssue: ok, agentEnrollment: ok, nodeList: ok},
			want:   v1.FleetCapabilities{JoinTokenIssuance: true, AgentEnrollment: true},
		},
		{name: "no handlers", routes: fleetPublicRoutes{}, want: v1.FleetCapabilities{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			got := mountFleetRoutes(mux, tc.routes)
			if got != tc.want {
				t.Fatalf("capability flags=%+v, want %+v", got, tc.want)
			}
			for _, route := range []struct {
				path    string
				handler http.Handler
			}{
				{path: "/api/v1/fleet/join-tokens", handler: tc.routes.joinTokenIssue},
				{path: "/api/v1/fleet/enroll", handler: tc.routes.agentEnrollment},
				{path: "/api/v1/fleet/nodes", handler: tc.routes.nodeList},
				{path: "/api/v1/fleet/nodes/node-a/credentials", handler: tc.routes.nodeCredentialRotate},
			} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, route.path, nil))
				wantStatus := http.StatusNotFound
				if route.handler != nil {
					wantStatus = http.StatusNoContent
				}
				if response.Code != wantStatus {
					t.Fatalf("route %s status=%d, want %d", route.path, response.Code, wantStatus)
				}
			}
		})
	}
}

type capabilityAgentStub struct {
	handler http.Handler
	flags   v1.FleetCapabilities
}

func (s *capabilityAgentStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *capabilityAgentStub) FleetCapabilitiesHandler(flags v1.FleetCapabilities) http.Handler {
	s.flags = flags
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func TestPostgresMuxCapabilitiesUseMountedRouteFlags(t *testing.T) {
	controlPlane := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	agents := &capabilityAgentStub{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	handler := postgresOnlyMux(controlPlane, agents)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/fleet/capabilities", nil))
	want := v1.FleetCapabilities{JoinTokenIssuance: true, AgentEnrollment: true, NodeCredentialRotation: true}
	if response.Code != http.StatusNoContent || agents.flags != want {
		t.Fatalf("capability route status=%d flags=%+v, want mounted flags %+v", response.Code, agents.flags, want)
	}
}

func TestPostgresPanelHandlerRequiresSafeContractMarker(t *testing.T) {
	legacy := postgresPanelHandler([]byte(`<html>Demo preview localStorage</html>`))
	response := httptest.NewRecorder()
	legacy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "Demo preview") {
		t.Fatal("legacy bundle was exposed instead of a visible unavailable page")
	}

	safe := []byte(`<meta name="porter-api-contract" content="postgres-only"><input type="password"><script>fetch('/api/v1/fleet/capabilities');fetch('/api/v1/fleet/join-tokens');fetch('/api/v1/fleet/enroll');fetch('/api/v1/fleet/nodes')</script>`)
	if !isPostgreSQLPanelBundle(safe) {
		t.Fatal("valid PG-only panel contract was rejected")
	}
	for _, forbidden := range []string{
		`<script>const sample={}</script>`, `<script>fetch('/v1/projects')</script>`, `<script>localStorage.setItem('token','x')</script>`,
	} {
		if isPostgreSQLPanelBundle(append(safe, []byte(forbidden)...)) {
			t.Fatal("panel bundle contract accepted sample, legacy, or persistent-token behavior")
		}
	}
}

func TestSystemdUnitsUseCredentialFilesForPorterUser(t *testing.T) {
	for _, tc := range []struct {
		name        string
		serviceType string
		credentials []string
		ordering    []string
	}{
		{name: "porter.service", serviceType: "simple", credentials: []string{
			"LoadCredential=porter-admin-token:/etc/porter/porter-admin-token",
			"LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn",
		}, ordering: []string{"Requires=porter-migrate.service", "After=porter-migrate.service"}},
		{name: "porter-migrate.service", serviceType: "oneshot", credentials: []string{
			"LoadCredential=porter-postgres-dsn:/etc/porter/porter-postgres-dsn",
			"LoadCredential=porter-admin-token:/etc/porter/porter-admin-token",
		}, ordering: []string{"Before=porter.service"}},
	} {
		contents, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "systemd", tc.name))
		if err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		unit := string(contents)
		requiredSettings := append([]string{"Type=" + tc.serviceType, "User=porter", "Group=porter", "UnsetEnvironment=PORTER_POSTGRES_DSN PORTER_ADMIN_TOKEN"}, tc.credentials...)
		requiredSettings = append(requiredSettings, tc.ordering...)
		for _, required := range requiredSettings {
			if !containsExactLine(unit, required) {
				t.Errorf("%s missing %q", tc.name, required)
			}
		}
		if strings.Contains(unit, "EnvironmentFile=") {
			t.Errorf("%s still reads an EnvironmentFile", tc.name)
		}
		for _, line := range strings.Split(unit, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "Environment=") && !strings.HasPrefix(line, "SetEnvironment=") && !strings.HasPrefix(line, "PassEnvironment=") && !strings.HasPrefix(line, "Exec") {
				continue
			}
			for _, secret := range []string{"PORTER_POSTGRES_DSN", "PORTER_ADMIN_TOKEN", "--dsn", "--admin-token"} {
				if strings.Contains(line, secret) {
					t.Errorf("%s contains forbidden secret fallback %q", tc.name, secret)
				}
			}
		}
		if tc.name == "porter.service" && (strings.Contains(unit, "ReadWritePaths=/var/lib/porter") || strings.Contains(unit, "StateDirectory=porter")) {
			t.Error("PostgreSQL-only server unit grants legacy local database state writes")
		}
	}
	makefile, err := os.ReadFile(filepath.Join("..", "..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read root Makefile: %v", err)
	}
	for _, required := range []string{
		"build: porter agent porter-migrate",
		"porter-migrate:",
		"-o ../$(BIN)/porter-migrate ./cmd/porter-migrate",
		"install: porter porter-migrate",
		"install -D -m 0755 $(BIN)/porter $(DESTDIR)/usr/local/bin/porter",
		"install -D -m 0755 $(BIN)/porter-migrate $(DESTDIR)/usr/local/bin/porter-migrate",
		"install -D -m 0644 deploy/systemd/porter.service $(DESTDIR)$(SYSTEMD_UNIT_DIR)/porter.service",
		"install -D -m 0644 deploy/systemd/porter-migrate.service $(DESTDIR)$(SYSTEMD_UNIT_DIR)/porter-migrate.service",
		"systemd >= 247 is required",
		"systemd --version",
		"[ \"$$version\" -ge 247 ]",
		"if [ -z \"$(DESTDIR)\" ]; then systemctl daemon-reload; fi",
	} {
		if !strings.Contains(string(makefile), required) {
			t.Errorf("root Makefile missing %q", required)
		}
	}
	envExample, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "porter.env.example"))
	if err != nil {
		t.Fatalf("read Porter environment example: %v", err)
	}
	if strings.Contains(string(envExample), "PORTER_DB=") || strings.Contains(string(envExample), "PORTER_ADMIN_TOKEN=") || strings.Contains(string(envExample), "PORTER_POSTGRES_DSN=") {
		t.Error("environment example includes a legacy database path or secret")
	}
	if _, err := systemdcred.ReadPostgresDSN(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "systemd >= 247") {
		t.Fatal("missing LoadCredential environment did not fail closed with the systemd minimum")
	}
}

func containsExactLine(contents, want string) bool {
	for _, line := range strings.Split(contents, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func startPostgresAPITestServer(t *testing.T, dsn string) (*httptest.Server, func(), http.Handler) {
	t.Helper()
	handler, closeDB, err := configureAPIRoutes("operator-token", dsn)
	if err != nil {
		t.Fatal("could not mount migrated PostgreSQL APIs")
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	t.Cleanup(closeDB)
	return server, closeDB, handler
}

func doPostgresAPIRequest(t *testing.T, client *http.Client, method, url string, body []byte, bearer string) *http.Response {
	t.Helper()
	var requestBody *strings.Reader
	if body == nil {
		requestBody = strings.NewReader("")
	} else {
		requestBody = strings.NewReader(string(body))
	}
	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("request to PostgreSQL API failed")
	}
	return response
}

func resetAPIMountTestDB(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`DROP SCHEMA IF EXISTS porter_control_plane CASCADE`,
		`DROP TABLE IF EXISTS public.porter_control_plane_schema_migrations`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reset disposable PostgreSQL test schema failed")
		}
	}
	return nil
}
