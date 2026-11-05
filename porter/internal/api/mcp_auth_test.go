package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

func TestMCPScopedTokenEnforcesCapabilitiesAndProjectScope(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	allowedProject, err := st.CreateProject(ctx, "allowed-project")
	if err != nil {
		t.Fatalf("create allowed project: %v", err)
	}
	blockedProject, err := st.CreateProject(ctx, "blocked-project")
	if err != nil {
		t.Fatalf("create blocked project: %v", err)
	}
	blockedApp, err := st.CreateApp(ctx, blockedProject.ID, "blocked-app", "example.invalid/app:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create blocked app: %v", err)
	}
	const adminToken = "test-admin-token"
	httpServer := httptest.NewServer(New(st, adminToken, "").Handler())
	defer httpServer.Close()

	// Scoped credentials must not replace or weaken the operator credential.
	unauthenticated, err := http.Get(httpServer.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET /mcp without auth: %v", err)
	}
	_ = unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /mcp status = %d, want 401", unauthenticated.StatusCode)
	}
	adminTools := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", adminToken, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	}, http.StatusOK)
	if !strings.Contains(string(adminTools), "porter_drain_node") {
		t.Fatalf("operator tool catalog is unexpectedly restricted: %s", adminTools)
	}

	created := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/v1/mcp/tokens", adminToken, "", map[string]interface{}{
		"name":         "ci-agent",
		"capabilities": []string{"projects:read", "apps:read", "apps:create"},
		"project_ids":  []string{allowedProject.ID},
	}, http.StatusCreated)
	var credential struct {
		Token      string         `json:"token"`
		Credential store.MCPToken `json:"credential"`
	}
	if err := json.Unmarshal(created, &credential); err != nil {
		t.Fatalf("decode created MCP credential: %v", err)
	}
	if credential.Token == "" || credential.Credential.ID == "" {
		t.Fatalf("credential response is incomplete: %s", created)
	}
	if strings.Contains(string(created), "token_hash") {
		t.Fatalf("credential response exposed its stored token hash: %s", created)
	}
	listed := doJSONRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/v1/mcp/tokens", adminToken, "", nil, http.StatusOK)
	if strings.Contains(string(listed), credential.Token) {
		t.Fatalf("credential listing exposed the bearer token: %s", listed)
	}

	tools := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	}, http.StatusOK)
	if !strings.Contains(string(tools), "porter_list_projects") || !strings.Contains(string(tools), "porter_create_app") || !strings.Contains(string(tools), "porter_get_autoscale") || strings.Contains(string(tools), "porter_drain_node") {
		t.Fatalf("scoped tool catalog does not match capabilities: %s", tools)
	}
	appDenied := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]interface{}{"name": "porter_get_autoscale", "arguments": map[string]interface{}{"app_id": blockedApp.ID}},
	}, http.StatusOK)
	if !strings.Contains(string(appDenied), `"isError":true`) || !strings.Contains(string(appDenied), "outside this token's scope") {
		t.Fatalf("app operation escaped project scope: %s", appDenied)
	}

	projectsResponse := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]interface{}{"name": "porter_list_projects", "arguments": map[string]interface{}{}},
	}, http.StatusOK)
	if !strings.Contains(string(projectsResponse), "allowed-project") || strings.Contains(string(projectsResponse), "blocked-project") {
		t.Fatalf("project list escaped token scope: %s", projectsResponse)
	}

	denied := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]interface{}{"name": "porter_create_app", "arguments": map[string]interface{}{
			"project_id": blockedProject.ID, "name": "must-not-exist", "image": "example.invalid/app:latest",
		}},
	}, http.StatusOK)
	if !strings.Contains(string(denied), `"isError":true`) || !strings.Contains(string(denied), "outside this token's scope") {
		t.Fatalf("out-of-scope mutation was not denied: %s", denied)
	}
	apps, err := st.ListApps(ctx, blockedProject.ID)
	if err != nil {
		t.Fatalf("list blocked project's apps: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "blocked-app" {
		t.Fatalf("denied MCP request created an out-of-scope app: %+v", apps)
	}

	allowed := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]interface{}{"name": "porter_create_app", "arguments": map[string]interface{}{
			"project_id": allowedProject.ID, "name": "allowed-app", "image": "example.invalid/app:latest",
		}},
	}, http.StatusOK)
	if strings.Contains(string(allowed), `"isError":true`) {
		t.Fatalf("in-scope capability call failed: %s", allowed)
	}
	apps, err = st.ListApps(ctx, allowedProject.ID)
	if err != nil || len(apps) != 1 || apps[0].Name != "allowed-app" {
		t.Fatalf("in-scope app creation missing: apps=%+v err=%v", apps, err)
	}

	doJSONRequest(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/v1/mcp/tokens/"+credential.Credential.ID, adminToken, "", nil, http.StatusOK)
	revokedRequest, err := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("create revoked-token request: %v", err)
	}
	revokedRequest.Header.Set("Authorization", "Bearer "+credential.Token)
	revokedResponse, err := httpServer.Client().Do(revokedRequest)
	if err != nil {
		t.Fatalf("send revoked-token request: %v", err)
	}
	_ = revokedResponse.Body.Close()
	if revokedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", revokedResponse.StatusCode)
	}
}

func TestMCPPolicyCoversCatalogAndKeepsFleetWritesOperatorOnly(t *testing.T) {
	for _, tool := range porterMCPTools() {
		name, _ := tool["name"].(string)
		if _, ok := mcpToolRules[name]; !ok {
			t.Errorf("advertised tool %q has no authorization rule", name)
		}
	}

	principal := mcpPrincipal{
		ID:           "restricted-agent",
		Capabilities: map[string]bool{"fleet:write": true},
		NodeIDs:      []string{"*"},
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	s := New(st, "operator-token", "")
	if err := s.authorizeMCPTool(context.Background(), principal, "porter_drain_node", json.RawMessage(`{"node_id":"node-1"}`)); err == nil || !strings.Contains(err.Error(), "operator-only") {
		t.Fatalf("restricted token was allowed to drain a node: %v", err)
	}
	if err := s.authorizeMCPTool(context.Background(), principal, "not_a_porter_tool", nil); err == nil {
		t.Fatal("unknown MCP tool was not denied")
	}
	if err := s.authorizeMCPTool(context.Background(), mcpPrincipal{Admin: true}, "porter_drain_node", json.RawMessage(`{"node_id":"node-1"}`)); err != nil {
		t.Fatalf("operator token was denied an operator-only tool: %v", err)
	}
}

func TestMCPNodeListFiltersNodeScope(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	allowedNodeID := enrollTestNode(t, ctx, st, "allowed-node")
	otherNodeID := enrollTestNode(t, ctx, st, "other-node")
	const adminToken = "test-admin-token"
	httpServer := httptest.NewServer(New(st, adminToken, "").Handler())
	defer httpServer.Close()
	created := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/v1/mcp/tokens", adminToken, "", map[string]interface{}{
		"name": "node-reader", "capabilities": []string{"fleet:read"}, "node_ids": []string{allowedNodeID},
	}, http.StatusCreated)
	var credential struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created, &credential); err != nil || credential.Token == "" {
		t.Fatalf("decode node reader credential: %s err=%v", created, err)
	}
	result := doJSONRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/mcp", credential.Token, "", map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]interface{}{"name": "porter_list_nodes", "arguments": map[string]interface{}{}},
	}, http.StatusOK)
	if !strings.Contains(string(result), allowedNodeID) || strings.Contains(string(result), otherNodeID) {
		t.Fatalf("node list escaped credential scope: %s", result)
	}
}

func enrollTestNode(t *testing.T, ctx context.Context, st *store.Store, hostname string) string {
	t.Helper()
	joinToken, err := st.CreateJoinToken(ctx, time.Hour)
	if err != nil {
		t.Fatalf("create join token: %v", err)
	}
	enrollment, err := st.Enroll(ctx, protocol.EnrollRequest{Token: joinToken, Hostname: hostname, OS: "linux", Arch: "amd64", CPUs: 2, MemoryMB: 2048, DiskMB: 1024})
	if err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return enrollment.NodeID
}

func TestMCPNarrowTokenCannotReadUnscopedDomainOrGlobalData(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	allowed, err := st.CreateProject(ctx, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := st.CreateProject(ctx, "blocked")
	if err != nil {
		t.Fatal(err)
	}
	allowedApp, err := st.CreateApp(ctx, allowed.ID, "allowed-app", "example.invalid/app:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create allowed app: %v", err)
	}
	blockedApp, err := st.CreateApp(ctx, blocked.ID, "blocked-app", "example.invalid/app:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create blocked app: %v", err)
	}
	if _, err := st.CreateDomain(ctx, allowed.ID, allowedApp.ID, "allowed.example.test", "custom"); err != nil {
		t.Fatalf("create allowed domain: %v", err)
	}
	if _, err := st.CreateDomain(ctx, blocked.ID, blockedApp.ID, "blocked.example.test", "custom"); err != nil {
		t.Fatalf("create blocked domain: %v", err)
	}
	s := New(st, "operator-token", "")
	p := mcpPrincipal{ID: "narrow", Capabilities: map[string]bool{
		"domains:read": true, "operations:read": true, "logs:read": true,
		"snapshots:read": true, "metrics:read": true, "gateway:read": true,
		"images:read": true, "fleet:read": true,
	}, ProjectIDs: []string{allowed.ID}, NodeIDs: []string{"allowed-node"}}

	if err := s.authorizeMCPTool(ctx, p, "porter_list_domains", json.RawMessage(`{}`)); err == nil {
		t.Fatal("domain list without project_id was allowed for a narrow project token")
	}
	if err := s.authorizeMCPTool(ctx, p, "porter_list_domains", json.RawMessage(`{"project_id":"`+blocked.ID+`"}`)); err == nil {
		t.Fatal("domain list for an out-of-scope project was allowed")
	}
	out, err := s.callMCPTool(httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(context.WithValue(ctx, mcpPrincipalContextKey{}, p)), "porter_list_domains", json.RawMessage(`{"project_id":"`+allowed.ID+`"}`))
	if err != nil {
		t.Fatalf("in-scope domain list failed: %v", err)
	}
	domains, ok := out.([]store.Domain)
	if !ok || len(domains) != 1 || domains[0].Hostname != "allowed.example.test" {
		t.Fatalf("in-scope domain result was not safely filtered: %#v", out)
	}

	deniedReads := []struct {
		tool string
		args string
	}{
		{"porter_list_operations", `{}`},
		{"porter_list_metrics", `{}`},
		{"porter_gateway_stats", `{}`},
		{"porter_gateway_requests", `{}`},
		{"porter_list_traces", `{}`},
		{"porter_list_images", `{}`},
		{"porter_list_logs", `{"vm_id":"unrelated-vm"}`},
		{"porter_list_snapshots", `{"vm_id":"unrelated-vm"}`},
		{"porter_get_node", `{"node_id":"other-node"}`},
		{"porter_node_metrics", `{"node_id":"other-node"}`},
		{"porter_node_logs", `{"node_id":"other-node"}`},
		{"porter_node_vms", `{"node_id":"other-node"}`},
	}
	for _, tc := range deniedReads {
		t.Run(tc.tool, func(t *testing.T) {
			if err := s.authorizeMCPTool(ctx, p, tc.tool, json.RawMessage(tc.args)); err == nil {
				t.Errorf("narrow token was authorized for %s", tc.tool)
			}
		})
	}
	for _, tc := range []struct{ tool, args string }{
		{"porter_list_logs", `{"vm_id":"other-vm"}`},
		{"porter_list_snapshots", `{"vm_id":"other-vm"}`},
	} {
		if err := s.authorizeMCPTool(ctx, mcpPrincipal{Admin: true}, tc.tool, json.RawMessage(tc.args)); err != nil {
			t.Errorf("administrator denied %s: %v", tc.tool, err)
		}
	}
	wildcard := p
	wildcard.ProjectIDs = []string{"*"}
	if err := s.authorizeMCPTool(ctx, wildcard, "porter_list_domains", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("wildcard project scope could not perform unfiltered domain list: %v", err)
	}
}
