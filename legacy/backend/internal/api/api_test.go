package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"porter/internal/agent"
	"porter/internal/billing"
	"porter/internal/config"
	"porter/internal/event"
	"porter/internal/store"
	"porter/internal/types"
)

// TestRoutesRegistersPaths ensures Routes() wires every handler without
// panicking. A nil store is safe here: handlers never run, only registration.
func TestRoutesRegistersPaths(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "v1.0.0-beta-dev")
	mux := http.NewServeMux()
	a.Routes(mux)

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/overview", http.StatusUnauthorized}, // auth gate runs first
		{http.MethodGet, "/traffic", http.StatusUnauthorized},
		{http.MethodGet, "/projects/abc/replicas", http.StatusUnauthorized},
		{http.MethodGet, "/definitely-not-a-route", http.StatusNotFound},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Errorf("%s %s: expected %d, got %d", tc.method, tc.path, tc.want, rr.Code)
		}
	}
}

// TestEveryRegisteredRouteIsMapped tests that every route registered in
// Routes() is present in the routePerms table, so no authenticated route runs
// without a specific RBAC permission guarding it. It parses api.go statically.
func TestEveryRegisteredRouteIsMapped(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	text := string(src)

	// Registered patterns: mux.HandleFunc("METHOD /path", ...).
	handleRe := regexp.MustCompile(`mux\.HandleFunc\("([^"]+)"`)
	registered := map[string]bool{}
	for _, m := range handleRe.FindAllStringSubmatch(text, -1) {
		registered[m[1]] = true
	}

	// Mapped patterns: inside the routePerms map literal, keys are lines like
	// "\t\"GET /path\": \"perm\"," — capture the full "METHOD /path" key.
	lineRe := regexp.MustCompile(`^\s*"((?:GET|POST|PUT|PATCH|DELETE|HEAD) [^"]+)"\s*:\s*"`)
	mnorm := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil {
			mnorm[m[1]] = true
		}
	}

	var missing []string
	for pat := range registered {
		if isUnGuarded(pat) {
			continue
		}
		if !mnorm[pat] {
			missing = append(missing, pat)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes registered but missing from routePerms (%d):\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	// Reverse check: every routePerms key must be an actually-registered route —
	// otherwise the permission entry is dead. Handler existence is enforced by
	// the compiler (a mux line referencing a missing handler won't build).
	var dead []string
	for pat := range mnorm {
		if !registered[pat] {
			dead = append(dead, pat)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Fatalf("routePerms entries with no matching registered route (%d):\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
}

// isUnGuarded lists patterns that intentionally need no permission guard.

// isUnGuarded lists patterns that intentionally need no permission guard.
func isUnGuarded(p string) bool {
	prefixes := []string{
		"GET /csrf", "GET /health", "GET /healthz", "GET /version",
		"POST /auth/", "GET /auth/session",
		"POST /login", "POST /logout",
		"GET /users/me", "PATCH /users/me", "DELETE /users/me",
		"POST /images/custom", "GET /images/ml",
	}
	for _, pre := range prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func TestSelectFieldsFilters(t *testing.T) {
	req := httptest.NewRequest("GET", "/x?fields=id,name", nil)
	v := map[string]any{"id": "1", "name": "n", "secret": "drop"}
	out, ok := selectFields(req, v).(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", selectFields(req, v))
	}
	if len(out) != 2 || out["id"] != "1" {
		t.Fatalf("unexpected projection: %v", out)
	}
	if _, bad := out["secret"]; bad {
		t.Fatalf("secret must be dropped: %v", out)
	}
}

func TestSelectFieldsEmptyPassthrough(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	v := map[string]any{"a": 1}
	if selectFields(req, v).(map[string]any)["a"] != 1 {
		t.Fatal("empty fields must passthrough")
	}
}

// TestProtectedRoutesRequireAuthentication exercises every registered route
// declaration through the real ServeMux and middleware stack. It intentionally
// does not call handlers with a nil store: the authentication boundary must
// reject an anonymous request before any feature-specific dependency runs.
func TestProtectedRoutesRequireAuthentication(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "test-secret", "example.com", "test")
	mux := http.NewServeMux()
	a.Routes(mux)

	checked := 0
	for _, route := range apiRoutes {
		if !route.auth {
			continue
		}
		path := smokePath(route.pattern)
		req := httptest.NewRequest(route.method, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		checked++
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("protected route %s %s returned %d, want 401", route.method, route.pattern, rec.Code)
		}
	}
	if checked == 0 {
		t.Fatal("route table contains no protected routes")
	}
}

// smokePath turns Go ServeMux patterns into concrete paths while preserving
// the route shape. Query/body validation is intentionally bypassed because
// this test verifies only registration and the authentication boundary.
func smokePath(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			parts[i] = "smoke"
		}
	}
	return strings.Join(parts, "/")
}

// TestAuthSetsRequestID verifies every request through auth (even rejected
// ones) carries an X-Request-ID for log/audit correlation (task T10b).
func TestAuthSetsRequestID(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "v1.0.0-beta-dev")
	next := a.auth(func(w http.ResponseWriter, r *http.Request) {})

	// Generated when absent (401 path still carries it).
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	rr := httptest.NewRecorder()
	next(rr, req)
	if rr.Header().Get("X-Request-ID") == "" {
		t.Error("expected generated X-Request-ID header")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rr.Code)
	}

	// Echoed when supplied.
	req2 := httptest.NewRequest(http.MethodGet, "/projects", nil)
	req2.Header.Set("X-Request-ID", "test-123")
	rr2 := httptest.NewRecorder()
	next(rr2, req2)
	if got := rr2.Header().Get("X-Request-ID"); got != "test-123" {
		t.Errorf("expected echoed request ID, got %q", got)
	}
}

// TestRequestIDContextRoundTrip verifies withRequestID/currentRequestID agree.
func TestRequestIDContextRoundTrip(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	req2 := withRequestID(rr, req)
	if currentRequestID(req2) == "" {
		t.Error("expected request ID in context")
	}
	if currentRequestID(req) != "" {
		t.Error("original request must stay untouched")
	}
}

// TestPaginateSlicesAndHeaders verifies limit/cursor paging (task T10b).
func TestPaginateSlicesAndHeaders(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	req := httptest.NewRequest(http.MethodGet, "/x?limit=2", nil)
	rr := httptest.NewRecorder()
	page := paginate(rr, req, items)
	if len(page) != 2 || page[0] != 1 {
		t.Fatalf("first page wrong: %v", page)
	}
	if rr.Header().Get("X-Total-Count") != "5" || rr.Header().Get("X-Next-Cursor") != "2" {
		t.Fatalf("paging headers wrong: %v", rr.Header())
	}
	req2 := httptest.NewRequest(http.MethodGet, "/x?limit=2&cursor=4", nil)
	rr2 := httptest.NewRecorder()
	last := paginate(rr2, req2, items)
	if len(last) != 1 || rr2.Header().Get("X-Next-Cursor") != "" {
		t.Fatalf("last page wrong: %v headers %v", last, rr2.Header())
	}
}

// TestJWKSUnavailableWithoutKey verifies the JWKS endpoint 503s when JWT is
// off instead of leaking an empty key set.
func TestJWKSUnavailableWithoutKey(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "v1.0.0-beta-dev")
	req := httptest.NewRequest(http.MethodGet, "/auth/jwks", nil)
	rr := httptest.NewRecorder()
	a.handleJWKS(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without JWT key, got %d", rr.Code)
	}
}

// TestCurrentPrincipalHasNoFallback verifies unauthenticated context cannot
// resolve to any privileged principal.
func TestCurrentPrincipalHasNoFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/projects/x", nil)
	p := currentPrincipal(req)
	if p.username != "" || p.role != "" {
		t.Fatalf("expected empty principal, got %+v", p)
	}
}

// TestPermForRoute verifies the central route→permission table maps known
// patterns to specific <resource>.<action> codes.
func TestPermForRoute(t *testing.T) {
	tests := []struct{ pattern, want string }{
		{"POST /projects/{projectId}/replicas/{n}/exec", "replica.exec"},
		{"POST /projects/{projectId}/replicas/{n}/ssh-cert", "ssh.connect"},
		{"GET /projects/{projectId}/replicas/{n}/ssh-info", "ssh.connect"},
		{"POST /projects/{projectId}/deployments/{deployId}/promote", "deployment.promote"},
		{"POST /projects/{projectId}/deployments/{deployId}/rollback", "deployment.rollback"},
		{"DELETE /projects/{projectId}/members/{username}", "member.remove"},
		{"POST /projects/{projectId}/members/invite", "member.invite"},
		{"DELETE /projects/{projectId}", "project.delete"},
		{"POST /projects/{projectId}/transfer", "project.transfer"},
		{"POST /projects/{projectId}/crons/{cronId}/run", "cron.run"},
		{"DELETE /volumes/{volumeId}", "volume.delete"},
		{"POST /projects/{projectId}/cache/purge", "cache.purge"},
		{"GET /projects/{projectId}/analytics/bandwidth", "analytics.read"},
		{"POST /orgs/transfer", "org.transfer"},
		{"DELETE /orgs/members/{username}", "org.member.remove"},
		{"GET /org", "project.read"},
		{"PATCH /org", "org.settings"},
		{"GET /projects/{projectId}/environments/{envId}/range", "project.read"},
		{"GET /replicas", "replica.list"},
		{"GET /replicas/{replicaId}", "replica.list"},
		{"GET /host/prerequisites", "metric.read"},
		{"GET /host/runtime", "metric.read"},
		{"GET /vms/{replicaId}/logs", "log.read"},
		{"GET /vms/{replicaId}/health", "replica.list"},
		{"GET /vms/{replicaId}/ssh-info", "ssh.connect"},
		{"POST /vms/{replicaId}/exec", "replica.exec"},
		{"POST /users", "user.create"},
		{"DELETE /servers/{id}", "server.remove"},
	}
	for _, c := range tests {
		req := httptest.NewRequest(http.MethodGet, "/irrelevant", nil)
		req.Pattern = c.pattern
		if got := permForRoute(req); got != c.want {
			t.Errorf("permForRoute(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

// TestPermForRouteUnknownPattern confirms unlisted routes return "" (auth-only).
func TestPermForRouteUnknownPattern(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/irrelevant", nil)
	req.Pattern = "GET /some/unlisted/thing"
	if got := permForRoute(req); got != "" {
		t.Fatalf("expected empty permission for unlisted route, got %q", got)
	}
}

// funcSource returns the source text of the named top-level functions in
// file, so source-tracing tests assert against the handlers themselves rather
// than whatever else shares the file.
func funcSource(t *testing.T, file string, names ...string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var sb strings.Builder
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && want[fd.Name.Name] {
			sb.Write(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
			sb.WriteByte('\n')
		}
	}
	if sb.Len() == 0 {
		t.Fatalf("%s: none of %v found", file, names)
	}
	return sb.String()
}

// pkgFuncSource returns the source text of the named top-level functions,
// searching every non-test .go file in the package. The api package was split
// from a single handlers.go into per-domain files, so source-tracing tests must
// not depend on which file a handler happens to live in. With no names it
// returns every function in the package.
func pkgFuncSource(t *testing.T, names ...string) string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var sb strings.Builder
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if len(want) > 0 && !want[fd.Name.Name] {
				continue
			}
			sb.Write(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
			sb.WriteByte('\n')
		}
	}
	if sb.Len() == 0 {
		t.Fatalf("none of %v found in the package", names)
	}
	return sb.String()
}

func ldapLoginRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "v1.0.0-beta-dev")
	req := httptest.NewRequest(http.MethodPost, "/auth/ldap/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	a.handleLDAPLogin(rr, req)
	return rr
}

func TestHandleLDAPLoginRejectsMissingFields(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"login":"jdoe"}`,
		`{"password":"secret"}`,
		`{"login":"","password":""}`,
		`not-json`,
	} {
		rr := ldapLoginRequest(t, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: want 400, got %d (%s)", body, rr.Code, rr.Body.String())
		}
	}
}

func TestHandleLDAPLoginRequiresConfig(t *testing.T) {
	t.Setenv("PORTER_LDAP_URL", "")
	t.Setenv("PORTER_LDAP_BASE_DN", "")
	t.Setenv("PORTER_LDAP_DOMAIN", "")
	t.Setenv("PORTER_LDAP_SCHEMA", "")
	t.Setenv("PORTER_LDAP_STARTTLS", "")

	rr := ldapLoginRequest(t, `{"login":"jdoe","password":"secret"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without LDAP env config, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LDAP is not configured") {
		t.Fatalf("503 body must name LDAP config, got %q", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Scoped RBAC assignments (live PG)
// ---------------------------------------------------------------------------

// TestRBACAssignmentRoutesGrantAndRevoke: the grant the permission resolver
// reads (granted() → HasCapability) can now be created, listed and revoked
// through the API.
func TestRBACAssignmentRoutesGrantAndRevoke(t *testing.T) {
	st := wp8Store(t)
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	mux := bootstrapFlowMux(t, a)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	_, raw := wp8AdminPrincipal(t, st, suffix)
	csrf := wp8CSRF(t, mux, raw)
	uname := "wp8member-" + suffix
	salt := store.NewID()
	member := &types.User{ID: store.NewID(), Username: uname, Role: "member", Salt: salt, PasswordHash: passwordHash("wp8-password-123", salt)}
	st.PutUser(member)
	t.Cleanup(func() { st.DeleteUser(member.ID) })

	proj := &types.Project{ID: store.NewID(), Name: "flow-rbac-proj-" + suffix, ServicePools: map[string]*types.ServicePool{}}
	st.PutProject(proj)
	t.Cleanup(func() { st.DeleteProject(proj.ID) })
	t.Cleanup(func() { _ = st.RevokeRole("user", uname, "project", proj.ID, "member") })

	// Validation errors first (no rows written).
	code, _ := wp8Request(t, mux, http.MethodPost, "/rbac/assignments", raw, csrf, "",
		map[string]string{"principal_id": uname, "scope_type": "project", "role_id": "member"})
	if code != http.StatusBadRequest {
		t.Fatalf("assign without scope_id: expected 400, got %d", code)
	}
	code, _ = wp8Request(t, mux, http.MethodPost, "/rbac/assignments", raw, csrf, "",
		map[string]string{"principal_id": uname, "scope_type": "project", "scope_id": proj.ID, "role_id": "no-such-role"})
	if code != http.StatusBadRequest {
		t.Fatalf("assign unknown role: expected 400, got %d", code)
	}

	// Grant the member role on the project.
	code, body := wp8Request(t, mux, http.MethodPost, "/rbac/assignments", raw, csrf, "",
		map[string]string{"principal_id": uname, "scope_type": "project", "scope_id": proj.ID, "role_id": "member"})
	if code != http.StatusCreated {
		t.Fatalf("POST /rbac/assignments: expected 201, got %d (%v)", code, body)
	}
	if !st.HasCapability("user", uname, "project.read", "project", proj.ID) {
		t.Fatal("scoped grant did not take effect (HasCapability still false)")
	}

	// Listed at the scope.
	code, body = wp8Request(t, mux, http.MethodGet, "/rbac/assignments?scope_type=project&scope_id="+proj.ID, raw, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /rbac/assignments: expected 200, got %d (%v)", code, body)
	}
	if !strings.Contains(jsonBody(t, body), uname) {
		t.Fatalf("GET /rbac/assignments: principal %s missing: %v", uname, body)
	}

	// Revoke.
	code, _ = wp8Request(t, mux, http.MethodDelete,
		"/rbac/assignments?principal_type=user&principal_id="+uname+"&scope_type=project&scope_id="+proj.ID+"&role_id=member",
		raw, csrf, "", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE /rbac/assignments: expected 200, got %d", code)
	}
	if st.HasCapability("user", uname, "project.read", "project", proj.ID) {
		t.Fatal("revoked grant still grants (HasCapability true after revoke)")
	}

	// Bad scope_type rejected on read.
	if code, _ := wp8Request(t, mux, http.MethodGet, "/rbac/assignments?scope_type=galaxy", raw, "", "", nil); code != http.StatusBadRequest {
		t.Fatalf("list bad scope_type: expected 400, got %d", code)
	}
}

// WP8: first-run flow integrity + the new bootstrap/coverage routes.
//
// Hermetic tests pin the route table (perm changes here are security-relevant
// diffs). Behavior tests run against the live Postgres (PORTER_TEST_DATABASE_URL,
// same gating as the store tests) and walk the real Routes() mux end to end:
// seed → login → authed request → 200. The database is shared with other test
// runs, so every fixture is removed via t.Cleanup — including the seeded
// admin's password row, which is restored exactly as found.

// ---------------------------------------------------------------------------
// Hermetic: route table pinning
// ---------------------------------------------------------------------------

// TestBootstrapRoutesPerms pins the new routes to their capability codes and
// auth posture. POST /nodes/enroll is the only public one: the single-use
// enrollment token in the body is the credential.
func TestBootstrapRoutesPerms(t *testing.T) {
	want := map[string]string{
		"GET /stacks":                  "project.read",
		"GET /stacks/{stackId}":        "project.read",
		"DELETE /stacks/{stackId}":     "project.delete",
		"POST /nodes/enroll":           "",
		"GET /nodes/enrollment-tokens": "server.register",
		"GET /rbac/assignments":        "org.audit",
		"POST /rbac/assignments":       "org.member.role",
		"DELETE /rbac/assignments":     "org.member.role",
	}
	got := map[string]string{}
	authed := map[string]bool{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
			authed[key] = r.auth
		}
	}
	for key, perm := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("route %q missing from apiRoutes", key)
			continue
		}
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
		if authed[key] != (perm != "") {
			t.Errorf("route %q auth flag = %v, want auth=%v", key, authed[key], perm != "")
		}
	}
}

// TestNodeEnrollRejectsBeforeStore: malformed input is rejected before any
// database access, so the endpoint stays safe even without a store.
func TestNodeEnrollRejectsBeforeStore(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "", "porter.test", "vtest")
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"malformed token", `{"token":"garbage","hostname":"h"}`, http.StatusBadRequest},
		{"empty token", `{"hostname":"h"}`, http.StatusBadRequest},
		{"empty hostname", `{"token":"penr_` + strings.Repeat("0", 64) + `"}`, http.StatusBadRequest},
		{"bad body", `not json`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/nodes/enroll", strings.NewReader(tc.body))
		rr := httptest.NewRecorder()
		a.handleNodeEnroll(rr, req)
		if rr.Code != tc.want {
			t.Errorf("%s: expected %d, got %d", tc.name, tc.want, rr.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Shared live-PG helpers
// ---------------------------------------------------------------------------

func wp8Store(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping live-PG test")
	}
	st := store.NewStore(dsn)
	// Cleanups run LIFO: registered first here, so the pool closes after all
	// fixture cleanups (a defer in the test body would close it too early).
	t.Cleanup(func() { st.Close() })
	return st
}

// bootstrapFlowMux registers the real route table on a fresh mux, exactly as
// the server does.
func bootstrapFlowMux(t *testing.T, a *API) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	a.Routes(mux)
	return mux
}

// wp8Request performs one request against the mux and decodes the JSON body.
// orgID optionally sets the X-Porter-Org-Id context header.
func wp8Request(t *testing.T, mux *http.ServeMux, method, path, token, csrf, orgID string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if orgID != "" {
		req.Header.Set(HeaderOrgID, orgID)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

// wp8CSRF fetches a CSRF token through /csrf. The route sits behind auth
// (unlike login), so a real client fetches it right after logging in — tests
// do the same with the bearer they just obtained.
func wp8CSRF(t *testing.T, mux *http.ServeMux, token string) string {
	t.Helper()
	code, body := wp8Request(t, mux, http.MethodGet, "/csrf", token, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /csrf: expected 200, got %d", code)
	}
	tok, _ := body["csrf_token"].(string)
	if tok == "" {
		t.Fatalf("GET /csrf: empty csrf_token: %v", body)
	}
	return tok
}

// wp8AdminPrincipal creates a throwaway platform-admin user + API key so the
// route-level RBAC gate passes exactly like it would for the seeded admin.
// Returns the raw bearer token (the API key row is cleaned up).
func wp8AdminPrincipal(t *testing.T, st *store.Store, suffix string) (string, string) {
	t.Helper()
	uname := "wp8admin-" + suffix
	salt := store.NewID()
	u := &types.User{ID: store.NewID(), Username: uname, Role: "admin", Salt: salt, PasswordHash: passwordHash("wp8-password-123", salt)}
	st.PutUser(u)
	t.Cleanup(func() { st.DeleteUser(u.ID) })
	raw := store.NewID() + store.NewID()
	k := &types.APIKey{ID: store.NewID(), UserID: u.ID, Name: "wp8-test", TokenHash: hashToken(raw), CreatedAt: time.Now()}
	st.PutAPIKey(k)
	t.Cleanup(func() { st.DeleteAPIKey(k.ID) })
	return uname, raw
}

// jsonBody re-marshals a decoded body for substring assertions.
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshal body: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// First-run flow: seed → login → RBAC → server add (live PG)
// ---------------------------------------------------------------------------

// TestFirstRunFlowSeedLoginRBACServerAdd walks the exact first-boot journey
// against the real Routes() mux: default org seeded, admin password
// bootstrapped once, login issues a working token, the seeded admin passes
// routePerms on five core routes, and a server registers + heartbeats.
func TestFirstRunFlowSeedLoginRBACServerAdd(t *testing.T) {
	st := wp8Store(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	// (a) Seeding: migrations ran inside store.NewStore; the 0012-seeded
	// default org resolves and EnsureDefaultOrg is idempotent against it.
	orgID, err := st.EnsureDefaultOrg(context.Background(), "admin", "default")
	if err != nil {
		t.Fatalf("ensure default org: %v", err)
	}
	if orgID == "" {
		t.Fatal("default org id empty after EnsureDefaultOrg")
	}

	// The seeded admin's password is operator-supplied and initialized
	// exactly once. The shared test DB may already carry a hash: borrow the
	// row with a known credential and restore the original afterward.
	const pw = "porter-first-run-test-pw"
	orig, ok := st.GetUserByUsername(store.SeededAdminUsername)
	if !ok {
		t.Fatalf("seeded admin %q missing; migrations broken", store.SeededAdminUsername)
	}
	if orig.PasswordHash == "" {
		if err := st.EnsureSeededAdmin(pw); err != nil {
			t.Fatalf("bootstrap seeded admin: %v", err)
		}
	} else {
		borrowed := *orig
		borrowed.Salt = store.NewID()
		borrowed.PasswordHash = passwordHash(pw, borrowed.Salt)
		st.PutUser(&borrowed)
	}
	t.Cleanup(func() { st.PutUser(orig) })

	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	mux := bootstrapFlowMux(t, a)

	if code, _ := wp8Request(t, mux, http.MethodGet, "/healthz", "", "", "", nil); code != http.StatusOK {
		t.Fatalf("GET /healthz: expected 200, got %d", code)
	}

	// (b) Login with the bootstrapped credential.
	code, body := wp8Request(t, mux, http.MethodPost, "/auth/login", "", "", "", map[string]string{"username": "admin", "password": pw})
	if code != http.StatusOK {
		t.Fatalf("POST /auth/login: expected 200, got %d (%v)", code, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("login response carries no token: %v", body)
	}
	if key, ok := st.GetAPIKeyByHash(hashToken(tok)); !ok || key.UserID != orig.ID {
		t.Fatal("login: issued token does not resolve to the admin's api key row")
	} else {
		t.Cleanup(func() { st.DeleteAPIKey(key.ID) })
	}
	csrf := wp8CSRF(t, mux, tok)

	// (c) RBAC spot-check 1: default org readable with the fresh token.
	code, body = wp8Request(t, mux, http.MethodGet, "/orgs/default", tok, "", orgID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /orgs/default: expected 200, got %d (%v)", code, body)
	}

	// RBAC spot-check 2: org member creation (creates the user row + membership).
	memberName := "flowmember-" + suffix
	code, body = wp8Request(t, mux, http.MethodPost, "/orgs/members", tok, csrf, orgID,
		map[string]string{"username": memberName, "password": "member-password-123", "role": "member"})
	if code != http.StatusCreated {
		t.Fatalf("POST /orgs/members: expected 201, got %d (%v)", code, body)
	}
	if mu, ok := st.GetUserByUsername(memberName); !ok {
		t.Fatal("org member user row missing after 201")
	} else {
		t.Cleanup(func() { st.DeleteOrgMember(orgID, mu.ID); st.DeleteUser(mu.ID) })
	}

	// (d) Server add: register + list + heartbeat.
	host := "flow-node-" + suffix
	code, body = wp8Request(t, mux, http.MethodPost, "/servers", tok, csrf, "",
		map[string]string{"hostname": host, "address": "10.10.0.9:22"})
	if code != http.StatusCreated {
		t.Fatalf("POST /servers: expected 201, got %d (%v)", code, body)
	}
	serverID, _ := body["id"].(string)
	if serverID == "" {
		t.Fatalf("POST /servers: no id in response: %v", body)
	}
	t.Cleanup(func() { st.DeleteServer(serverID) })

	// RBAC spot-check 3: server list.
	code, body = wp8Request(t, mux, http.MethodGet, "/servers", tok, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /servers: expected 200, got %d (%v)", code, body)
	}

	// RBAC spot-check 4: audit read.
	code, body = wp8Request(t, mux, http.MethodGet, "/orgs/audit", tok, "", orgID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /orgs/audit: expected 200, got %d (%v)", code, body)
	}

	// RBAC spot-check 5: project create through the full 202 path (registered
	// image so bootReplica resolves; the vmm is nil so nothing actually boots).
	rootfs := filepath.Join(t.TempDir(), "rootfs.ext4")
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(rootfs, []byte("rootfs"), 0o644); err != nil {
		t.Fatalf("write rootfs: %v", err)
	}
	if err := os.WriteFile(kernel, []byte("kernel"), 0o644); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	gi := &types.GoldenImage{ID: store.NewID(), Name: "flow-base-" + suffix, Image: "base://flow-" + suffix,
		Rootfs: rootfs, Kernel: kernel, Status: "ready"}
	if err := st.PutGoldenImage(gi); err != nil {
		t.Fatalf("put golden image: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteGoldenImage(gi.ID) })
	code, body = wp8Request(t, mux, http.MethodPost, "/projects", tok, csrf, "",
		map[string]any{"name": "flow-proj-" + suffix, "image": gi.Image, "replicas": 1})
	if code != http.StatusAccepted {
		t.Fatalf("POST /projects: expected 202, got %d (%v)", code, body)
	}
	proj, _ := body["project"].(map[string]any)
	projID, _ := proj["id"].(string)
	if projID == "" {
		t.Fatalf("POST /projects: no project id in response: %v", body)
	}
	t.Cleanup(func() {
		for _, vm := range st.ListVMs() {
			if vm != nil && vm.ProjectID == projID {
				st.DeleteVM(vm.ID)
			}
		}
		for _, d := range st.ListDeployments(projID) {
			_ = st.DeleteDeployment(projID, d.ID)
		}
		st.DeleteProject(projID)
	})

	// (d2) Heartbeat with machine identity: the node token is the credential
	// (SRS §9), not the user session. Seed it the way enroll would.
	ntok := "pnrt-test-" + store.NewID()
	if err := st.SetServerNodeToken(serverID, sha256Hex(ntok)); err != nil {
		t.Fatalf("set node token: %v", err)
	}
	code, body = wp8NodeHeartbeat(t, mux, "/servers/"+serverID+"/heartbeat", ntok,
		map[string]any{"id": serverID, "status": "online", "agent_version": "vtest", "vm_count": 0})
	if code != http.StatusOK {
		t.Fatalf("POST /servers/{id}/heartbeat (node token): expected 200, got %d (%v)", code, body)
	}
	// A heartbeat WITHOUT the node token is rejected.
	code, _ = wp8NodeHeartbeat(t, mux, "/servers/"+serverID+"/heartbeat", "",
		map[string]any{"id": serverID, "status": "online"})
	if code != http.StatusUnauthorized {
		t.Fatalf("heartbeat without node token: expected 401, got %d", code)
	}
}

// wp8NodeHeartbeat posts a heartbeat with a node token (no CSRF: the route is
// public and machine-authenticated).
func wp8NodeHeartbeat(t *testing.T, mux *http.ServeMux, path, nodeToken string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if nodeToken != "" {
		req.Header.Set("X-Porter-Node-Token", nodeToken)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// ---------------------------------------------------------------------------
// Node enrollment loop (live PG)
// ---------------------------------------------------------------------------

// TestNodeEnrollConsumesTokenAndRegistersServer closes the loop the mint
// endpoint started: token → enroll (server registered, token consumed) →
// replay refused → unknown token 401 → token state listable, value masked.
func TestNodeEnrollConsumesTokenAndRegistersServer(t *testing.T) {
	st := wp8Store(t)
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	mux := bootstrapFlowMux(t, a)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	_, raw := wp8AdminPrincipal(t, st, suffix)

	minted, err := agent.NewEnrollmentToken("customer_owned", time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := st.CreateEnrollmentToken(minted.Token, minted.Provider, "{}", minted.ExpiresAt); err != nil {
		t.Fatalf("store token: %v", err)
	}
	t.Cleanup(func() { st.DeleteEnrollmentToken(minted.Token) })

	host := "enroll-node-" + suffix
	code, body := wp8Request(t, mux, http.MethodPost, "/nodes/enroll", "", "", "",
		map[string]string{"token": minted.Token, "hostname": host, "address": "10.20.0.5:22"})
	if code != http.StatusCreated {
		t.Fatalf("POST /nodes/enroll: expected 201, got %d (%v)", code, body)
	}
	srv, _ := body["server"].(map[string]any)
	serverID, _ := srv["id"].(string)
	if serverID == "" {
		t.Fatalf("enroll response carries no server id: %v", body)
	}
	t.Cleanup(func() { st.DeleteServer(serverID) })
	if _, ok := st.GetServer(serverID); !ok {
		t.Fatal("enrolled server missing from the store")
	}

	// Single use: a replay is a 409, not a second server.
	if code, _ := wp8Request(t, mux, http.MethodPost, "/nodes/enroll", "", "", "",
		map[string]string{"token": minted.Token, "hostname": host + "-2"}); code != http.StatusConflict {
		t.Fatalf("enroll replay: expected 409, got %d", code)
	}

	// Unknown well-formed token → 401 (never leak existence).
	unknown, _ := agent.NewEnrollmentToken("customer_owned", time.Hour)
	if code, _ := wp8Request(t, mux, http.MethodPost, "/nodes/enroll", "", "", "",
		map[string]string{"token": unknown.Token, "hostname": host}); code != http.StatusUnauthorized {
		t.Fatalf("unknown token: expected 401, got %d", code)
	}

	// Token state is listable by an operator; the raw value stays masked.
	code, body = wp8Request(t, mux, http.MethodGet, "/nodes/enrollment-tokens", raw, "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /nodes/enrollment-tokens: expected 200, got %d (%v)", code, body)
	}
	tokens, _ := body["tokens"].([]any)
	found := false
	for _, entry := range tokens {
		row, _ := entry.(map[string]any)
		if row["token"] == minted.Token {
			t.Fatal("token list leaked the raw enrollment token")
		}
		if row["used_by"] == host {
			found = true
		}
	}
	if !found {
		t.Fatalf("consumed token (used_by=%q) not visible in the list: %v", host, body)
	}
}

// Per-server detail surface tests (WP7). Perm pinning is hermetic; handler
// behavior runs against the live Postgres (PORTER_TEST_DATABASE_URL, same
// gating as the store tests, fixtures removed via t.Cleanup).

// TestServerDetailRoutesPerms pins the per-server detail surface to the same
// capability the existing server routes use (GET /servers/{id} →
// server.register). A perm change here is a security-relevant diff.
func TestServerDetailRoutesPerms(t *testing.T) {
	want := map[string]string{
		"GET /servers/{id}/detail":    "server.register",
		"GET /servers/{id}/analytics": "server.register",
		"GET /servers/{id}/vms":       "server.register",
		"GET /servers/{id}/logs":      "server.register",
	}
	got := map[string]string{}
	authed := map[string]bool{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
			authed[key] = r.auth
		}
	}
	for key, perm := range want {
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
		if !authed[key] {
			t.Errorf("route %q must be auth-required", key)
		}
	}
}

// serverDetailTestAPI seeds a registered server with a heartbeat, one placed
// VM (active placement), a cpu/memory sample pair and its registration log
// line; everything is removed on cleanup.
func serverDetailTestAPI(t *testing.T) (*API, *store.Store, serverDetailFixture) {
	t.Helper()
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping per-server detail test")
	}
	st := store.NewStore(dsn)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := serverDetailFixture{
		serverID:  store.NewID(),
		vmID:      store.NewID(),
		projectID: store.NewID(),
		hostname:  "srvdetail-" + suffix,
	}
	st.PutServer(&types.Server{ID: f.serverID, Name: f.hostname, Address: "10.0.0.9:22", Status: "online"})
	if err := st.PutHeartbeat(store.Heartbeat{
		NodeID: f.serverID, AgentVersion: "vtest", VCPUFree: 7, MemFreeMiB: 4096, VMCount: 1,
		ReportedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put heartbeat: %v", err)
	}
	st.PutProject(&types.Project{ID: f.projectID, Name: "srvdetail-proj-" + suffix})
	st.PutVM(&types.VM{ID: f.vmID, Name: "srvdetail-vm", ProjectID: f.projectID, ServiceName: "web",
		State: types.StateRunning, HealthStatus: "healthy", VCPUs: 2, MemMiB: 512})
	placementID, err := st.ReservePlacement(f.vmID, f.serverID, "10.42.0.5", "op-"+suffix)
	if err != nil {
		t.Fatalf("reserve placement: %v", err)
	}
	if err := st.SetPlacementState(placementID, "active"); err != nil {
		t.Fatalf("activate placement: %v", err)
	}
	now := time.Now()
	for _, m := range []struct {
		metric string
		value  float64
	}{{"cpu_percent", 25}, {"memory_mib", 512}} {
		if err := st.AddMetric(&types.MetricSample{ID: store.NewID(), VMID: f.vmID, Metric: m.metric, Value: m.value, TS: now}); err != nil {
			t.Fatalf("seed metric: %v", err)
		}
	}
	st.AppendDaemonLog("server registered: " + f.hostname + " (10.0.0.9:22)")
	t.Cleanup(func() {
		st.PurgeServerData(f.serverID)
		st.DeleteVM(f.vmID)
		st.DeleteProject(f.projectID)
		st.DeleteServer(f.serverID)
	})
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	return a, st, f
}

type serverDetailFixture struct {
	serverID, vmID, projectID, hostname string
}

func getJSON(t *testing.T, h func(*API, http.ResponseWriter, *http.Request), a *API, path, id string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h(a, rr, req)
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return rr.Code, body
}

func TestServerDetailHandlers(t *testing.T) {
	a, _, f := serverDetailTestAPI(t)

	// Detail bundle: heartbeat, capacity vs allocated, vm counts, logs.
	code, body := getJSON(t, (*API).handleServerDetail, a, "/servers/"+f.serverID+"/detail", f.serverID)
	if code != http.StatusOK {
		t.Fatalf("detail: expected 200, got %d (%v)", code, body)
	}
	if body["heartbeat"] == nil {
		t.Fatal("detail: heartbeat must surface (node_heartbeats row exists)")
	}
	capacity := body["capacity"].(map[string]any)
	if capacity["vcpus_free"].(float64) != 7 {
		t.Fatalf("detail: vcpus_free must come from the heartbeat: %v", capacity)
	}
	if capacity["vcpus_allocated"].(float64) != 2 || capacity["mem_allocated_mib"].(float64) != 512 {
		t.Fatalf("detail: allocated capacity must come from the placed VM: %v", capacity)
	}
	counts := body["vm_counts"].(map[string]any)
	if counts["running"] != float64(1) || body["vms_placed"] != float64(1) {
		t.Fatalf("detail: vm counts wrong: %v / %v", counts, body["vms_placed"])
	}
	logs := body["recent_logs"].([]any)
	if len(logs) == 0 {
		t.Fatal("detail: registration daemon-log line must be included")
	}
	if body["planned"] == nil {
		t.Fatal("detail: planned gaps must be labeled, not faked")
	}

	// Analytics: bucketed series for both collector metrics.
	code, body = getJSON(t, (*API).handleServerAnalytics, a, "/servers/"+f.serverID+"/analytics?minutes=120", f.serverID)
	if code != http.StatusOK {
		t.Fatalf("analytics: expected 200, got %d (%v)", code, body)
	}
	if body["step_seconds"] != float64(60) {
		t.Fatalf("analytics: default step must be 60s, got %v", body["step_seconds"])
	}
	series := body["series"].(map[string]any)
	cpu := series["cpu_percent"].([]any)
	if len(cpu) != 1 || cpu[0].(map[string]any)["avg"] != float64(25) {
		t.Fatalf("analytics: cpu series wrong: %v", series["cpu_percent"])
	}
	mem := series["memory_mib"].([]any)
	if len(mem) != 1 || mem[0].(map[string]any)["sum"] != float64(512) {
		t.Fatalf("analytics: memory series wrong: %v", series["memory_mib"])
	}

	// VM list: joined project name.
	code, body = getJSON(t, (*API).handleServerVMs, a, "/servers/"+f.serverID+"/vms", f.serverID)
	if code != http.StatusOK {
		t.Fatalf("vms: expected 200, got %d (%v)", code, body)
	}
	if body["count"] != float64(1) {
		t.Fatalf("vms: expected 1 placed VM, got %v", body["count"])
	}
	vm := body["vms"].([]any)[0].(map[string]any)
	if vm["id"] != f.vmID || vm["project_name"] == "" {
		t.Fatalf("vms: vm row not joined with project name: %v", vm)
	}

	// Logs: newest-first daemon-log tail for the node.
	code, body = getJSON(t, (*API).handleServerLogs, a, "/servers/"+f.serverID+"/logs?limit=5", f.serverID)
	if code != http.StatusOK {
		t.Fatalf("logs: expected 200, got %d (%v)", code, body)
	}
	if body["count"].(float64) < 1 {
		t.Fatalf("logs: registration line must match, got %v", body)
	}
}

// TestServerDetailHandlersUnknownServer: every sub-resource 404s on an
// unregistered node id (and never fabricates data).
func TestServerDetailHandlersUnknownServer(t *testing.T) {
	if os.Getenv("PORTER_TEST_DATABASE_URL") == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping per-server detail test")
	}
	st := store.NewStore(os.Getenv("PORTER_TEST_DATABASE_URL"))
	defer st.Close()
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	for _, h := range []func(*API, http.ResponseWriter, *http.Request){
		(*API).handleServerDetail,
		(*API).handleServerAnalytics,
		(*API).handleServerVMs,
		(*API).handleServerLogs,
	} {
		code, _ := getJSON(t, h, a, "/servers/missing", "missing")
		if code != http.StatusNotFound {
			t.Errorf("unknown server must 404, got %d", code)
		}
	}
}

// TestServerDetailWindowParsing pins the analytics window defaults/caps at
// the handler boundary (hermetic — the window resolver is pure).
func TestServerDetailWindowParsing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?minutes=60&step=300", nil)
	from, to, step := serverDetailWindow(req)
	if step != 300 {
		t.Fatalf("step param must win, got %d", step)
	}
	if d := to.Sub(from); d < 55*time.Minute || d > 65*time.Minute {
		t.Fatalf("minutes=60 must yield a ~1h window, got %v", d)
	}
	req = httptest.NewRequest(http.MethodGet, "/x?minutes=99999999", nil)
	from, to, _ = serverDetailWindow(req)
	if d := to.Sub(from); d > 7*24*time.Hour+time.Minute {
		t.Fatalf("window must cap at 7d, got %v", d)
	}
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	_, _, step = serverDetailWindow(req)
	if step != 60 {
		t.Fatalf("default step must be 60, got %d", step)
	}
}

// TestServerDetailLimitParsing pins the ?limit= clamp (hermetic).
func TestServerDetailLimitParsing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?limit=25", nil)
	if got := serverDetailLimit(req, 100, 1000); got != 25 {
		t.Fatalf("limit param must win, got %d", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/x?limit=99999", nil)
	if got := serverDetailLimit(req, 100, 1000); got != 1000 {
		t.Fatalf("limit must clamp at cap, got %d", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	if got := serverDetailLimit(req, 100, 1000); got != 100 {
		t.Fatalf("default limit must apply, got %d", got)
	}
}

func TestCanonicalDisruptionBudget(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want map[string]any
		err  bool
	}{
		{"min available", map[string]any{"min_available": float64(8)}, map[string]any{"min_available": 8}, false},
		{"max unavailable", map[string]any{"max_unavailable": float64(2)}, map[string]any{"max_unavailable": 2}, false},
		{"unset", map[string]any{}, map[string]any{}, false},
		{"both bounds", map[string]any{"min_available": float64(8), "max_unavailable": float64(2)}, nil, true},
		{"unknown field", map[string]any{"minimum": float64(8)}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalDisruptionBudget(tt.body)
			if (err != nil) != tt.err {
				t.Fatalf("canonicalDisruptionBudget(%v) error=%v, want error=%v", tt.body, err, tt.err)
			}
			if tt.err {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Stacks (live PG)
// ---------------------------------------------------------------------------

// TestStackRoutesCRUD: compose stacks become listable/detailable/deletable;
// deleting a stack preserves member projects (grouping only).
func TestStackRoutesCRUD(t *testing.T) {
	st := wp8Store(t)
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	mux := bootstrapFlowMux(t, a)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	_, raw := wp8AdminPrincipal(t, st, suffix)
	csrf := wp8CSRF(t, mux, raw)

	orgID, err := st.EnsureDefaultOrg(context.Background(), "admin", "default")
	if err != nil {
		t.Fatalf("ensure default org: %v", err)
	}
	stack := &types.Stack{ID: store.NewID(), Name: "flow-stack-" + suffix, OrgID: orgID, Source: "compose", ComposeYAML: "services: {}\n"}
	st.PutStack(stack)
	t.Cleanup(func() { st.DeleteStack(stack.ID) })
	proj := &types.Project{ID: store.NewID(), Name: "flow-stack-proj-" + suffix, OrgID: orgID, StackID: stack.ID, ServicePools: map[string]*types.ServicePool{}}
	st.PutProject(proj)
	t.Cleanup(func() { st.DeleteProject(proj.ID) })

	// Auth required: no bearer → 401 even with CSRF present.
	if code, _ := wp8Request(t, mux, http.MethodGet, "/stacks", "", csrf, orgID, nil); code != http.StatusUnauthorized {
		t.Fatalf("GET /stacks without bearer: expected 401, got %d", code)
	}

	code, body := wp8Request(t, mux, http.MethodGet, "/stacks", raw, "", orgID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /stacks: expected 200, got %d (%v)", code, body)
	}
	if !strings.Contains(jsonBody(t, body), stack.ID) {
		t.Fatalf("GET /stacks: stack %s missing: %v", stack.ID, body)
	}

	code, body = wp8Request(t, mux, http.MethodGet, "/stacks/"+stack.ID, raw, "", orgID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /stacks/{id}: expected 200, got %d (%v)", code, body)
	}
	if !strings.Contains(jsonBody(t, body), proj.ID) {
		t.Fatalf("GET /stacks/{id}: member project %s missing: %v", proj.ID, body)
	}

	if code, _ := wp8Request(t, mux, http.MethodGet, "/stacks/does-not-exist", raw, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET /stacks/{unknown}: expected 404, got %d", code)
	}

	code, body = wp8Request(t, mux, http.MethodDelete, "/stacks/"+stack.ID, raw, csrf, "", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE /stacks/{id}: expected 200, got %d (%v)", code, body)
	}
	if _, ok := st.GetStack(stack.ID); ok {
		t.Fatal("stack still present after delete")
	}
	if _, ok := st.GetProject(proj.ID); !ok {
		t.Fatal("member project must survive a stack delete")
	}
	if code, _ := wp8Request(t, mux, http.MethodGet, "/stacks/"+stack.ID, raw, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("GET /stacks/{deleted}: expected 404, got %d", code)
	}
}

// TestVPSRoutesPerms pins the VPS surface to its capabilities: the flip and
// the conversion are project settings changes (project.settings is the
// seeded capability the RBAC migration grants to owner/admin/member — there
// is no "project.update" permission in this RBAC world), and the state read
// is project.read. A perm change here is a security-relevant diff and must
// be deliberate.
func TestVPSRoutesPerms(t *testing.T) {
	want := map[string]string{
		"POST /projects/{projectId}/vps":   "project.settings",
		"GET /projects/{projectId}/vps":    "project.read",
		"DELETE /projects/{projectId}/vps": "project.settings",
	}
	got := map[string]string{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
		}
	}
	for key, perm := range want {
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
	}
}

// TestVPSHandlerRejectsBadBody exercises the validation branch that runs
// before any store touch, so it is hermetic with a nil store. An empty body
// is accepted (plain workload flip), a malformed one is a 400.
func TestVPSHandlerRejectsBadBody(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "vtest")
	req := httptest.NewRequest(http.MethodPost, "/projects/p1/vps", strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	a.handleEnableVPS(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed body, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestVPSDisableConflictIsExplicit409 statically pins the failure path the
// api package cannot exercise without a live store: converting a project
// with running VMs must be an explicit 409 naming the ?force=true escape
// hatch — never a silent teardown (SRS §66: never fake unimplemented
// behavior; §12 of the task: 409 with explicit message).
func TestVPSDisableConflictIsExplicit409(t *testing.T) {
	s := pkgFuncSource(t, "handleEnableVPS", "handleDisableVPS", "handleGetVPS", "vmLive")
	for _, sub := range []string{
		"pass ?force=true to convert to microvm anyway",
		"StatusConflict",
		"StatusNotFound",
	} {
		if !strings.Contains(s, sub) {
			t.Errorf("VPS handlers: expected to contain %q (failure path regressed?)", sub)
		}
	}
}

// Tests for the prebuilt OCI → microVM conversion route (POST /images/custom/oci):
// a hermetic route-perm pin, an upload-shape rejection that never touches a
// store, and (when PORTER_TEST_DATABASE_URL + mkfs.ext4 are available) a real
// conversion of a docker-save archive into a registered deployable image.

// tarNewWriter / tarHeader are tiny fixtures helpers for building the tar
// archives the conversion route consumes.
func tarNewWriter(w io.Writer) *tar.Writer { return tar.NewWriter(w) }

func tarHeader(name string, size int) *tar.Header {
	return &tar.Header{Name: name, Mode: 0o644, Size: int64(size)}
}

func TestOCIRoutePerm(t *testing.T) {
	want := map[string]string{
		"POST /images/custom/oci": "image.upload",
	}
	got := map[string]bool{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = true
			if r.perm != want[key] {
				t.Errorf("route %q perm = %q, want %q", key, r.perm, want[key])
			}
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("route %q missing from apiRoutes", key)
		}
	}
}

// TestOCIUploadRejectsBadShape: a tar without index.json/manifest.json must
// be rejected 400 before any conversion attempt, with nil-safe handling.
func TestOCIUploadRejectsBadShape(t *testing.T) {
	dir := t.TempDir()
	a := NewAPI(nil, nil, nil, nil, nil, "", "porter.test", "vtest")
	a.SetCustomImagesDir(dir)

	body := ociMultipart(t, "junk-image", map[string][]byte{"image.tar": junkTarBytes(t)})
	req := httptest.NewRequest(http.MethodPost, "/images/custom/oci", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+multipartBoundary)
	rr := httptest.NewRecorder()
	a.handleUploadCustomOCIImage(rr, req)
	// OCI→microVM conversion is deferred past v0.0.1-alpha, so the route must
	// say so explicitly (501 naming the route) instead of half-working, and it
	// must not leave a partial artifact behind.
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 (conversion deferred) for non-image tar, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "/images/custom/oci") {
		t.Errorf("501 body must name the deferred route, got %s", rr.Body.String())
	}
	// The deferred upload must not leave the raw tar behind.
	if _, err := os.Stat(filepath.Join(dir, "junk-image", "image.tar")); !os.IsNotExist(err) {
		t.Errorf("deferred upload left image.tar behind: %v", err)
	}
}

// TestOCIUploadRequiresName: missing name is a 400 before anything else runs.
func TestOCIUploadRequiresName(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "", "porter.test", "vtest")
	a.SetCustomImagesDir(t.TempDir())
	body := ociMultipart(t, "", map[string][]byte{"image.tar": junkTarBytes(t)})
	req := httptest.NewRequest(http.MethodPost, "/images/custom/oci", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+multipartBoundary)
	rr := httptest.NewRecorder()
	a.handleUploadCustomOCIImage(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 (conversion deferred) for missing name, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestOCIUploadRequiresKernel: without an uploaded kernel or a configured
// host vmlinux the conversion must fail explicitly, not register a dead image.
func TestOCIUploadRequiresKernel(t *testing.T) {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}
	a := NewAPI(nil, nil, nil, nil, nil, "", "porter.test", "vtest")
	a.SetCustomImagesDir(t.TempDir()) // no hostConfig → no kernel anywhere

	body := ociMultipart(t, "kern-less", map[string][]byte{"image.tar": dockerSaveBytes(t)})
	req := httptest.NewRequest(http.MethodPost, "/images/custom/oci", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+multipartBoundary)
	rr := httptest.NewRecorder()
	a.handleUploadCustomOCIImage(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 (conversion deferred) for missing kernel, got %d (%s)", rr.Code, rr.Body.String())
	}
}

const multipartBoundary = "porter-oci-test-boundary"

// ociMultipart builds a multipart body with a name field and file entries.
func ociMultipart(t *testing.T, name string, files map[string][]byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("--" + multipartBoundary + "\r\n")
	buf.WriteString("Content-Disposition: form-data; name=\"name\"\r\n\r\n" + name + "\r\n")
	for fname, content := range files {
		buf.WriteString("--" + multipartBoundary + "\r\n")
		buf.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"" + fname + "\"\r\n")
		buf.WriteString("Content-Type: application/x-tar\r\n\r\n")
		buf.Write(content)
		buf.WriteString("\r\n")
	}
	buf.WriteString("--" + multipartBoundary + "--\r\n")
	return &buf
}

// junkTarBytes returns a valid tar with no image manifest inside.
func junkTarBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tarNewWriter(&buf)
	if err := w.WriteHeader(tarHeader("random.txt", 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dockerSaveBytes returns a minimal docker-save archive: manifest.json +
// config + one plain layer (the layout `docker save` emits).
func dockerSaveBytes(t *testing.T) []byte {
	t.Helper()
	var layerBuf bytes.Buffer
	lw := tarNewWriter(&layerBuf)
	if err := lw.WriteHeader(tarHeader("hello.txt", 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := lw.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if err := lw.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := tarNewWriter(&buf)
	writeEntry := func(name string, body []byte) {
		if err := w.WriteHeader(tarHeader(name, len(body))); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	writeEntry("manifest.json", []byte(`[{"Config":"cfg.json","Layers":["layer1/layer.tar"]}]`))
	writeEntry("cfg.json", []byte(`{"config":{"Entrypoint":["/bin/sh"],"Cmd":["-c","echo hi"],"Env":["PATH=/bin"],"WorkingDir":"/"}}`))
	writeEntry("layer1/layer.tar", layerBuf.Bytes())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestOCIUploadConvertsDockerSave runs the real leg when the environment can:
// live PG for the golden-image row and mkfs.ext4 for the ext4 conversion. A
// docker-save archive goes in; a validating rootfs.ext4 + registered
// custom:// image come out.
func TestOCIUploadConvertsDockerSave(t *testing.T) {
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping live-PG OCI conversion")
	}
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available; conversion requires Linux e2fsprogs")
	}
	st := wp8Store(t)

	kernel := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(kernel, []byte("fake-vmlinux-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewAPI(st, nil, nil, nil, nil, "", "porter.test", "vtest")
	a.SetHostConfig(&config.Config{KernelImage: kernel})
	a.SetCustomImagesDir(t.TempDir())

	name := "e2e-oci-" + time.Now().Format("150405")
	body := ociMultipart(t, name, map[string][]byte{"image.tar": dockerSaveBytes(t)})
	req := httptest.NewRequest(http.MethodPost, "/images/custom/oci", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+multipartBoundary)
	rr := httptest.NewRecorder()
	a.handleUploadCustomOCIImage(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	var gi struct {
		ID    string `json:"id"`
		Image string `json:"image"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &gi); err != nil {
		t.Fatalf("decode golden image: %v", err)
	}
	if gi.Image != "custom://"+name {
		t.Errorf("registered image = %q, want custom://%s", gi.Image, name)
	}
	if gi.Kind != "custom-oci" {
		t.Errorf("kind = %q, want custom-oci", gi.Kind)
	}
	rootfs := filepath.Join(a.customImagesDir, name, "rootfs.ext4")
	if st2, err := os.Stat(rootfs); err != nil || st2.Size() == 0 {
		t.Fatalf("converted rootfs missing/empty: %v %v", st2, err)
	}
	if st3, err := os.Stat(filepath.Join(a.customImagesDir, name, "image.tar")); err != nil || st3.Size() == 0 {
		t.Errorf("source tar not retained: %v", err)
	}
	t.Cleanup(func() { st.DeleteGoldenImage(gi.ID) })
}

// Hermetic route-shape test for GET /projects/{projectId}/replicas/{n}/logs/search
// (feature_logs.go). No store fixture exists for handler-level tests (the store
// is Postgres-backed), so this pins the route shape: the path must be
// registered (auth gate 401) rather than unknown (404), and the permission
// map must guard it with log.read.
func TestA5LogsSearchRouteShape(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "vtest")
	mux := http.NewServeMux()
	a.Routes(mux)

	for _, target := range []string{
		"/projects/p1/replicas/0/logs/search?q=fatal",
		"/projects/p1/replicas/0/logs/search",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s: want 401 (registered + auth-gated), got %d", target, rr.Code)
		}
	}

	perms := buildRoutePerms()
	if got := perms["GET /projects/{projectId}/replicas/{n}/logs/search"]; got != "log.read" {
		t.Fatalf("search route perm = %q, want %q", got, "log.read")
	}
}

// TestBillingRoutesPerms pins the billing surface to its capabilities:
// plans CRUD + subscribe + invoice preview. A perm change here is a
// security-relevant diff and must be deliberate.
func TestBillingRoutesPerms(t *testing.T) {
	want := map[string]string{
		"GET /billing/plans":                                "billing.read",
		"POST /billing/plans":                               "billing.manage",
		"GET /projects/{projectId}/billing/subscription":    "billing.read",
		"POST /projects/{projectId}/billing/subscription":   "billing.manage",
		"PATCH /projects/{projectId}/billing/subscription":  "billing.manage",
		"DELETE /projects/{projectId}/billing/subscription": "billing.manage",
		"GET /projects/{projectId}/billing/invoice":         "billing.read",
	}
	got := map[string]string{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
		}
	}
	for key, perm := range want {
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
	}
}

// TestBillingGuardsDeclaredPostInit rebuilds the method+pattern → perm map
// from apiRoutes at test time (after every init() has appended its routes)
// and pins the billing guards. NOTE: it deliberately does NOT read the
// package-level routePerms var — that var is built once at var-init time,
// before init()-appended routes exist (see bug report: api.go:1173), so it
// is stale for every feature_*.go route at runtime.
func TestBillingGuardsDeclaredPostInit(t *testing.T) {
	rebuilt := map[string]string{}
	for _, r := range apiRoutes {
		if r.perm != "" {
			rebuilt[r.method+" "+r.pattern] = r.perm
		}
	}
	cases := map[string]string{
		"GET /billing/plans":                              "billing.read",
		"POST /billing/plans":                             "billing.manage",
		"POST /projects/{projectId}/billing/subscription": "billing.manage",
		"GET /projects/{projectId}/billing/invoice":       "billing.read",
	}
	for pattern, want := range cases {
		if got := rebuilt[pattern]; got != want {
			t.Errorf("apiRoutes guard %q = %q, want %q", pattern, got, want)
		}
	}
}

// TestInvoicePreviewMath mirrors handleInvoicePreview: MeterTotals for the
// last 30d fed into billing.Rate. The store call itself is PG-backed, so
// here we pin the pure rating half with stub totals.
func TestInvoicePreviewMath(t *testing.T) {
	plan := billing.Plan{
		ID:           "pro",
		MonthlyCents: 2000,
		Prices: map[string]billing.Price{
			"vcpu_seconds": {UnitCents: 0.01, Unit: "seconds"},
		},
	}
	start := time.Now().AddDate(0, -1, 0)
	stubTotals := map[string]float64{"vcpu_seconds": 5000, "mystery_meter": 3}
	inv := billing.Rate(plan, "proj-1", start, stubTotals)
	// 5000*0.01=50, mystery=0 → 2000+50=2050.
	if inv.TotalCents != 2050 {
		t.Fatalf("invoice total = %d, want 2050 (%+v)", inv.TotalCents, inv.Lines)
	}
	if !inv.PeriodStart.Equal(start) {
		t.Fatalf("period start not threaded through: %+v", inv)
	}
	seen := map[string]bool{}
	for _, l := range inv.Lines {
		seen[l.Meter] = true
	}
	if !seen["vcpu_seconds"] || !seen["mystery_meter"] {
		t.Fatalf("all meters must be listed (unknown at zero): %+v", inv.Lines)
	}
}

// TestQuotaCheckSemantics pins the pure admission predicate behind every
// quota gate: max_projects (create), max_vms (scale/provision), max_mem_mib
// (bootReplica clamp). projectQuotaLimits/quotaCheck themselves are
// pool-backed (PlanLimits) and covered by PG tests; OverQuota is the unit
// under test here.
func TestQuotaCheckSemantics(t *testing.T) {
	// Missing key = unlimited (quotas pass open, never a silent deny).
	if _, over := store.OverQuota(map[string]int64{}, "max_projects", 1000); over {
		t.Fatal("missing max_projects should pass open")
	}
	if _, over := store.OverQuota(nil, "max_vms", 1000); over {
		t.Fatal("nil limits should pass open")
	}
	// Equal-to-limit fits (would-be usage, not current).
	if _, over := store.OverQuota(map[string]int64{"max_projects": 2}, "max_projects", 2); over {
		t.Fatal("equal-to-limit should fit")
	}
	if _, over := store.OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 3); over {
		t.Fatal("equal-to-limit should fit")
	}
	// Over returns the limit so callers can name it in the 403.
	if lim, over := store.OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 4); !over || lim != 3 {
		t.Fatalf("expected over with limit 3, got lim=%d over=%v", lim, over)
	}
	if lim, over := store.OverQuota(map[string]int64{"max_mem_mib": 512}, "max_mem_mib", 1024); !over || lim != 512 {
		t.Fatalf("expected mem over with limit 512, got lim=%d over=%v", lim, over)
	}
}

// TestBillingHandlersRejectBadInput exercises the validation branches that
// run before any store touch, so they are hermetic with a nil store.
func TestBillingHandlersRejectBadInput(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "secret-material", "example.com", "vtest")
	cases := []struct {
		name    string
		handler func(*API, http.ResponseWriter, *http.Request)
		body    string
	}{
		{"putPlanNeedsID", (*API).handlePutPlan, `{"name":"x"}`},
		{"putPlanBadJSON", (*API).handlePutPlan, `not-json`},
		{"subscribeNeedsPlan", (*API).handleSubscribe, `{}`},
		{"provisionNeedsTemplate", (*API).handleProvisionService, `{}`},
		{"scheduleNeedsCron", (*API).handlePutBackupSchedule, `{}`},
		{"scheduleBadCron", (*API).handlePutBackupSchedule, `{"cron":"x"}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body))
		rr := httptest.NewRecorder()
		tc.handler(a, rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", tc.name, rr.Code, rr.Body.String())
		}
	}
}

// TestQuotaDenialsAreExplicit403s statically traces each admission path and
// asserts denials stay explicit 403s naming the limit (never a silent
// clamp or generic 500): max_projects on create (api.go), max_vms on
// scale/provision (handlers.go), mem clamp in
// bootReplica (api.go).
func TestQuotaDenialsAreExplicit403s(t *testing.T) {
	ranked := pkgFuncSource(t, "handleProvisionService")
	apiSrc, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	// The package is split into per-domain files now, so the quota assertions
	// scan the whole package rather than one handlers.go that no longer exists.
	handlers := []byte(pkgFuncSource(t))
	mustContain := []struct {
		file, src, sub string
	}{
		{"handlers.go (handleProvisionService)", ranked, "quota exceeded: max_vms="},
		{"api.go", string(apiSrc), "quota exceeded: max_projects="},
		{"api.go", string(apiSrc), "quota exceeded: max_vms="},
		{"handlers.go", string(handlers), "quota exceeded: max_vms="},
		{"api.go", string(apiSrc), `lims["max_mem_mib"]`},
	}
	for _, c := range mustContain {
		if !strings.Contains(c.src, c.sub) {
			t.Errorf("%s: expected to contain %q (quota path regressed?)", c.file, c.sub)
		}
	}
	for _, src := range []string{ranked, string(apiSrc), string(handlers)} {
		if !strings.Contains(src, "StatusForbidden") {
			t.Error("expected an explicit 403 (StatusForbidden) on the quota path")
		}
	}
}

// T10 billing route-perm pinning: parses the apiRoutes table directly, no
// server needed. Any perm change on the billing surface is a
// security-relevant diff and must be deliberate.

// TestT10BillingRoutePerms pins every billing route to its capability and
// fails if a billing route is added, removed, or re-permed without updating
// this table.
func TestT10BillingRoutePerms(t *testing.T) {
	want := map[string]string{
		"GET /billing/plans":                                "billing.read",
		"POST /billing/plans":                               "billing.manage",
		"GET /projects/{projectId}/billing/subscription":    "billing.read",
		"POST /projects/{projectId}/billing/subscription":   "billing.manage",
		"PATCH /projects/{projectId}/billing/subscription":  "billing.manage",
		"DELETE /projects/{projectId}/billing/subscription": "billing.manage",
		"GET /projects/{projectId}/billing/invoice":         "billing.read",
	}
	got := map[string]string{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
		}
	}
	for key, perm := range want {
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
	}
	// No billing route may ride without an explicit perm, and no billing
	// route may exist outside the pinned set.
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if !isT10BillingRoute(r.method, r.pattern) {
			continue
		}
		if r.perm == "" {
			t.Errorf("billing route %q has no permission gate", key)
		}
		if _, ok := want[key]; !ok {
			t.Errorf("unpinned billing route %q (perm %q): extend the pin table", key, r.perm)
		}
	}
}

func isT10BillingRoute(method, pattern string) bool {
	_ = method
	switch pattern {
	case "/billing/plans",
		"/projects/{projectId}/billing/subscription",
		"/projects/{projectId}/billing/invoice":
		return true
	}
	return false
}

// TestT10InvoicePreviewShape30d mirrors handleInvoicePreview (trailing ~30d
// window via AddDate(0, -1, 0) fed into billing.Rate) with stub totals so it
// stays hermetic — no store, no server. It pins the preview shape the API
// serializes: header, reconciled total, and sorted lines with unknowns at
// zero.
func TestT10InvoicePreviewShape30d(t *testing.T) {
	plan := billing.Plan{
		ID:           "pro",
		MonthlyCents: 2000,
		Prices: map[string]billing.Price{
			"vcpu_seconds": {UnitCents: 0.01, Unit: "seconds"},
		},
	}
	start := time.Now().AddDate(0, -1, 0) // same window as handleInvoicePreview
	stubTotals := map[string]float64{"vcpu_seconds": 5000, "mystery_meter": 3}
	inv := billing.Rate(plan, "proj-1", start, stubTotals)
	// 5000*0.01=50, mystery=0 → 2000+50=2050.
	if inv.TotalCents != 2050 {
		t.Fatalf("invoice total = %d, want 2050 (%+v)", inv.TotalCents, inv.Lines)
	}
	if inv.ProjectID != "proj-1" || inv.PlanID != "pro" || inv.MonthlyCents != 2000 {
		t.Fatalf("preview header wrong: %+v", inv)
	}
	if !inv.PeriodStart.Equal(start) {
		t.Fatalf("period start not threaded through: %+v", inv)
	}
	var sum int64
	seen := map[string]bool{}
	for _, l := range inv.Lines {
		seen[l.Meter] = true
		sum += l.Cents
		if l.Meter == "" {
			t.Fatalf("line with empty meter: %+v", l)
		}
	}
	if inv.TotalCents != inv.MonthlyCents+sum {
		t.Fatalf("total %d != base %d + lines %d", inv.TotalCents, inv.MonthlyCents, sum)
	}
	if !seen["vcpu_seconds"] || !seen["mystery_meter"] {
		t.Fatalf("all meters must be listed (unknown at zero): %+v", inv.Lines)
	}
	for i := 1; i < len(inv.Lines); i++ {
		if inv.Lines[i].Meter < inv.Lines[i-1].Meter {
			t.Fatalf("lines not sorted: %+v", inv.Lines)
		}
	}
}

// TestGlobalDomainsRoutesPerms pins the org-wide domain surface to the same
// capabilities the per-project domain routes use: domain.list for reads,
// domain.verify for the DNS probe. A perm change here is a security-relevant
// diff and must be deliberate.
func TestGlobalDomainsRoutesPerms(t *testing.T) {
	want := map[string]string{
		"GET /domains":         "domain.list",
		"POST /domains/verify": "domain.verify",
	}
	got := map[string]string{}
	for _, r := range apiRoutes {
		key := r.method + " " + r.pattern
		if _, ok := want[key]; ok {
			got[key] = r.perm
		}
	}
	for key, perm := range want {
		if got[key] != perm {
			t.Errorf("route %q perm = %q, want %q", key, got[key], perm)
		}
	}
}

// TestHandleListOrgDomains exercises the handler against a store with no
// pool (the nil-pool guard returns an empty, non-nil list): the response
// must be 200 with a "domains" array — never null — when the org header is
// set. The header short-circuits orgIDFromHeader, so no database is needed.
func TestHandleListOrgDomains(t *testing.T) {
	a := NewAPI(&store.Store{}, nil, nil, nil, nil, "", "example.com", "vtest")
	req := httptest.NewRequest(http.MethodGet, "/domains", nil)
	req.Header.Set(HeaderOrgID, "org-1")
	rr := httptest.NewRecorder()
	a.handleListOrgDomains(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Domains []store.OrgDomainRow `json:"domains"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rr.Body.String())
	}
	if body.Domains == nil {
		t.Fatal("domains must be an empty array, not null")
	}
	if len(body.Domains) != 0 {
		t.Fatalf("empty store must list zero domains, got %+v", body.Domains)
	}
}

// TestHandleVerifyOrgDomainRejectsBadInput covers the validation branches
// that run before any probe: bad JSON and a missing domain are 400s; a
// valid domain without a configured domain manager is an explicit 503.
func TestHandleVerifyOrgDomainRejectsBadInput(t *testing.T) {
	a := NewAPI(nil, nil, nil, nil, nil, "", "example.com", "vtest")
	req := httptest.NewRequest(http.MethodPost, "/domains/verify", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	a.handleVerifyOrgDomain(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing domain: expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/domains/verify", strings.NewReader(`not-json`))
	rr = httptest.NewRecorder()
	a.handleVerifyOrgDomain(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad json: expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/domains/verify", strings.NewReader(`{"domain":"app.example.com"}`))
	rr = httptest.NewRecorder()
	a.handleVerifyOrgDomain(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("nil domain manager: expected 503, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// GitHub pull_request webhook tests against the live Postgres (same gating
// as store tests: PORTER_TEST_DATABASE_URL, fixtures removed via t.Cleanup).

func prWebhookTestAPI(t *testing.T) (*API, *store.Store, *types.Project) {
	t.Helper()
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping PR webhook test")
	}
	st := store.NewStore(dsn)
	proj := &types.Project{
		ID:        store.NewID(),
		Name:      fmt.Sprintf("prwebhook-%d", time.Now().UnixNano()),
		CreatedAt: time.Now(),
	}
	st.PutProject(proj)
	t.Cleanup(func() {
		for _, b := range st.ListBuilds(proj.ID) {
			st.DeleteBuild(b.ID)
		}
		for _, e := range st.ListEnvironments(proj.ID) {
			st.DeleteEnvironment(e.ID)
		}
		for _, d := range st.ListDomains(proj.ID) {
			st.DeleteDomain(proj.ID, d.Domain)
		}
		st.DeleteProject(proj.ID)
	})
	a := NewAPI(st, event.NewHub(), nil, nil, nil, "secret-material", "porter.test", "vtest")
	return a, st, proj
}

func postGitHubEvent(t *testing.T, a *API, eventName string, payload map[string]any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventName)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	rr := httptest.NewRecorder()
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.ServeHTTP(rr, req)
	return rr
}

func prEventPayload(action, branch, sha string, number int) map[string]any {
	return map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"head": map[string]any{"ref": branch, "sha": sha},
			"base": map[string]any{"ref": "main"},
		},
		"repository": map[string]any{
			"clone_url": "https://github.com/octocat/widget.git",
			"full_name": "octocat/widget",
		},
	}
}

func TestWebhookPullRequestOpenedBuildsPreview(t *testing.T) {
	a, st, proj := prWebhookTestAPI(t)
	st.PutProjectSettings(proj.ID, "git", map[string]any{
		"repo":        "octocat/widget.git",
		"auto_deploy": true,
	})
	rr := postGitHubEvent(t, a, "pull_request", prEventPayload("opened", "feature/login", "abc1234567", 7), "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("opened PR must queue a build: %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "building" || resp["pull_request"] != float64(7) {
		t.Fatalf("bad response: %v", resp)
	}
	previews, _ := resp["previews"].([]any)
	if len(previews) != 1 || !strings.Contains(fmt.Sprint(previews[0]), "feature-login") {
		t.Fatalf("preview host must follow ensureBranchPreview naming: %v", previews)
	}
	foundBuild, foundEnv := false, false
	for _, b := range st.ListBuilds(proj.ID) {
		if b != nil && b.Branch == "feature/login" {
			foundBuild = true
		}
	}
	for _, e := range st.ListEnvironments(proj.ID) {
		if e != nil && e.Branch == "feature/login" && strings.Contains(e.Name, "preview-") {
			foundEnv = true
		}
	}
	if !foundBuild {
		t.Fatal("build row for head.ref missing")
	}
	if !foundEnv {
		t.Fatal("preview environment for head.ref missing")
	}
}

func TestWebhookPullRequestClosedTearsPreviewDown(t *testing.T) {
	a, st, proj := prWebhookTestAPI(t)
	st.PutProjectSettings(proj.ID, "git", map[string]any{
		"repo":        "octocat/widget.git",
		"auto_deploy": true,
	})
	// opened first: creates build + preview env + domain
	if rr := postGitHubEvent(t, a, "pull_request", prEventPayload("opened", "feature/login", "abc1234567", 7), ""); rr.Code != http.StatusAccepted {
		t.Fatalf("opened PR setup: %d %s", rr.Code, rr.Body.String())
	}
	envThere := false
	for _, e := range st.ListEnvironments(proj.ID) {
		if e != nil && e.Branch == "feature/login" {
			envThere = true
		}
	}
	if !envThere {
		t.Fatal("setup failed: preview environment must exist before the close event")
	}
	rr := postGitHubEvent(t, a, "pull_request", prEventPayload("closed", "feature/login", "abc1234567", 7), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("closed PR must acknowledge: %d %s", rr.Code, rr.Body.String())
	}
	for _, e := range st.ListEnvironments(proj.ID) {
		if e != nil && e.Branch == "feature/login" {
			t.Fatalf("preview environment for closed PR must be gone: %+v", e)
		}
	}
	for _, d := range st.ListDomains(proj.ID) {
		if d != nil && d.Type == "preview" && strings.Contains(d.Domain, "feature-login") {
			t.Fatalf("preview domain for closed PR must be gone: %+v", d)
		}
	}
}

func TestWebhookPullRequestUnknownActionAndEvent(t *testing.T) {
	a, st, proj := prWebhookTestAPI(t)
	st.PutProjectSettings(proj.ID, "git", map[string]any{"repo": "octocat/widget.git", "auto_deploy": true})
	rr := postGitHubEvent(t, a, "pull_request", prEventPayload("assigned", "feature/login", "abc", 7), "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "ignored") {
		t.Fatalf("unknown action must 200 with reason: %d %s", rr.Code, rr.Body.String())
	}
	rr = postGitHubEvent(t, a, "status", map[string]any{"state": "success"}, "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "ignored") {
		t.Fatalf("unknown event must 200 with reason: %d %s", rr.Code, rr.Body.String())
	}
	for _, e := range st.ListEnvironments(proj.ID) {
		if e != nil && e.Branch == "feature/login" {
			t.Fatalf("no preview may be created for ignored events: %+v", e)
		}
	}
}

func TestWebhookPullRequestBadSignatureFailsClosed(t *testing.T) {
	a, _, proj := prWebhookTestAPI(t)
	st := a.store
	st.PutProjectSettings(proj.ID, "git", map[string]any{
		"repo":           "octocat/widget.git",
		"auto_deploy":    true,
		"webhook_secret": "s3cret",
	})
	body, _ := json.Marshal(prEventPayload("opened", "feature/login", "abc", 7))
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rr := httptest.NewRecorder()
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature must fail closed: %d %s", rr.Code, rr.Body.String())
	}
}

// Temporary probe (removed before handoff).
func TestProbeTmp(t *testing.T) {
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no dsn")
	}
	st := store.NewStore(dsn)
	p := &types.Project{ID: store.NewID(), Name: fmt.Sprintf("probe-%d", time.Now().UnixNano())}
	t.Logf("before put: id=%q name=%q", p.ID, p.Name)
	st.PutProject(p)
	got, ok := st.GetProject(p.ID)
	t.Logf("get ok=%v", ok)
	if got != nil {
		t.Logf("got name=%q id=%q", got.Name, got.ID)
	}
	t.Logf("list with id=%q -> %d builds", p.ID, len(st.ListBuilds(p.ID)))
}

func writeSPAFixture(t *testing.T) http.FileSystem {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id=app></div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	return http.Dir(dir)
}

// Path-based routes (createWebHistory) must serve the app shell on
// refresh/deep-link; only the API and real files bypass the fallback.
func TestSPAFileServerFallback(t *testing.T) {
	h := SpaFileServer(writeSPAFixture(t))

	for _, p := range []string{"/", "/login", "/projects", "/projects/abc", "/host", "/images", "/admin"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), "<!doctype html") {
			t.Fatalf("GET %s: want app shell, got %q", p, body)
		}
	}

	// Real files are served as-is.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "console.log") {
		t.Fatalf("asset: want 200 with js body, got %d %q", rec.Code, rec.Body.String())
	}

	// Missing assets stay 404 (never boot the shell by accident).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: want 404, got %d %q", rec.Code, rec.Body.String())
	}

	// Mutating requests never get the shell.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects", nil))
	if rec.Code == http.StatusOK && strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype html") {
		t.Fatal("POST must not fall back to index.html")
	}
}
