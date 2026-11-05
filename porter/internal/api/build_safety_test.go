package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildImageRequiresExplicitUnisolatedBuildOptIn(t *testing.T) {
	t.Setenv(unisolatedBuildOptIn, "")
	req := httptest.NewRequest("POST", "/v1/images/build", strings.NewReader(`{"source_type":"docker","source_ref":"alpine:latest"}`))
	response := httptest.NewRecorder()
	(&Server{}).buildImage(response, req)
	if response.Code != 503 {
		t.Fatalf("status=%d want 503; body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), unisolatedBuildOptIn) {
		t.Fatalf("response does not explain the operator override: %s", response.Body.String())
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
