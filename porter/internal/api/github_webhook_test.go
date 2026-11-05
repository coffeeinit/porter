package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"porter/controlplane/internal/store"
)

func signedWebhookRequest(secret, body string) *http.Request {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-test")
	return req
}

func TestGitHubWebhookFailsClosedWithoutSecret(t *testing.T) {
	t.Setenv("PORTER_GITHUB_SECRET", "")
	rr := httptest.NewRecorder()
	New(nil, "", "").Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(`{}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestGitHubWebhookRejectsInvalidSignature(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("PORTER_GITHUB_SECRET", secret)
	req := signedWebhookRequest(secret, `{}`)
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	rr := httptest.NewRecorder()
	New(nil, "", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestGitHubWebhookRejectsOversizedBody(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("PORTER_GITHUB_SECRET", secret)
	body := strings.Repeat("x", (1<<20)+1)
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rr := httptest.NewRecorder()
	New(nil, "", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
}

func TestGitHubWebhookRejectsMalformedSignedJSON(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("PORTER_GITHUB_SECRET", secret)
	rr := httptest.NewRecorder()
	New(nil, "", "").Handler().ServeHTTP(rr, signedWebhookRequest(secret, `{`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestGitHubWebhookBuildRequiresExplicitUnisolatedBuildOptIn(t *testing.T) {
	const secret = "test-secret"
	t.Setenv("PORTER_GITHUB_SECRET", secret)
	t.Setenv(unisolatedBuildOptIn, "")
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	project, err := st.CreateProject(ctx, "webhook-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(ctx, project.ID, "webhook-app", "built://pending", 80, 1, 256, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	const repo = "https://example.invalid/porter.git"
	if err := st.SetAppSource(ctx, app.ID, repo, "main"); err != nil {
		t.Fatalf("set app source: %v", err)
	}
	body := `{"after":"` + strings.Repeat("a", 40) + `","ref":"refs/heads/main","repository":{"clone_url":"` + repo + `"}}`
	req := signedWebhookRequest(secret, body)
	response := httptest.NewRecorder()
	New(st, "", "").Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "isolated builder") {
		t.Fatalf("status=%d want 503 build guard; body=%s", response.Code, response.Body.String())
	}
	images, err := st.ListImages(ctx)
	if err != nil || len(images) != 0 {
		t.Fatalf("webhook created images despite default-off guard: images=%+v err=%v", images, err)
	}
}
