package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"porter/controlplane/internal/store"
)

func TestBuildImageRequiresExplicitUnisolatedBuildOptIn(t *testing.T) {
	t.Setenv(unisolatedBuildOptIn, "")
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	server := New(st, "", "")
	req := httptest.NewRequest("POST", "/v1/images/build", strings.NewReader(`{"name":"blocked","source_type":"docker","source_ref":"alpine:latest"}`))
	response := httptest.NewRecorder()
	server.buildImage(response, req)
	if response.Code != 503 {
		t.Fatalf("status=%d want 503; body=%s", response.Code, response.Body.String())
	}
	wantGuidance := "set " + unisolatedBuildOptIn + "=true only for trusted sources"
	if !strings.Contains(response.Body.String(), wantGuidance) {
		t.Fatalf("response does not explain the operator override: %s", response.Body.String())
	}
	images, err := st.ListImages(context.Background())
	if err != nil || len(images) != 0 {
		t.Fatalf("blocked build created image records: images=%+v err=%v", images, err)
	}
	server.buildMu.Lock()
	building := len(server.building)
	server.buildMu.Unlock()
	if building != 0 {
		t.Fatalf("blocked build launched %d build tasks, want none", building)
	}
}

func TestUnisolatedBuildOptInIsExplicit(t *testing.T) {
	for _, value := range []string{"", "yes", "1", "false"} {
		t.Setenv(unisolatedBuildOptIn, value)
		if unisolatedBuildsEnabled() {
			t.Fatalf("value %q unexpectedly enables unisolated builds", value)
		}
	}
	for _, value := range []string{"true", " TRUE "} {
		t.Setenv(unisolatedBuildOptIn, value)
		if !unisolatedBuildsEnabled() {
			t.Fatalf("value %q should enable explicit operator override", value)
		}
	}
}

func TestMCPBuildToolRequiresExplicitUnisolatedBuildOptIn(t *testing.T) {
	t.Setenv(unisolatedBuildOptIn, "")
	ctx := context.WithValue(context.Background(), mcpPrincipalContextKey{}, mcpPrincipal{Admin: true})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)).WithContext(ctx)
	_, err := (&Server{}).callMCPTool(req, "porter_build_image", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "isolated builder") {
		t.Fatalf("MCP build error = %v, want default-off isolation guard", err)
	}
}
