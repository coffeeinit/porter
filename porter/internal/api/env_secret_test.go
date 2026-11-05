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

func TestListAppEnvMasksSecretValues(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	project, err := st.CreateProject(context.Background(), "env-redaction-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(context.Background(), project.ID, "web", "example.invalid:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	const secretValue = "do-not-return-this-secret"
	if err := st.PutAppEnv(context.Background(), app.ID, "API_TOKEN", secretValue, true); err != nil {
		t.Fatalf("save secret env: %v", err)
	}
	if err := st.PutAppEnv(context.Background(), app.ID, "APP_MODE", "production", false); err != nil {
		t.Fatalf("save regular env: %v", err)
	}

	const adminToken = "test-admin-token"
	httpServer := httptest.NewServer(New(st, adminToken, "").Handler())
	defer httpServer.Close()
	body := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/apps/"+app.ID+"/env", adminToken, "", nil, http.StatusOK)
	if strings.Contains(string(body), secretValue) {
		t.Fatalf("secret value was returned by the API: %s", body)
	}
	var env []store.AppEnv
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode environment response: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("got %d env entries, want 2", len(env))
	}
	byKey := make(map[string]store.AppEnv, len(env))
	for _, entry := range env {
		byKey[entry.Key] = entry
	}
	if entry := byKey["API_TOKEN"]; !entry.Secret || entry.Value != "••••••••" {
		t.Errorf("secret should be marked and masked, got %+v", entry)
	}
	if entry := byKey["APP_MODE"]; entry.Secret || entry.Value != "production" {
		t.Errorf("non-secret value should remain readable, got %+v", entry)
	}
}
