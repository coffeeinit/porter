package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"porter/agent/internal/client"
)

func TestPrepareSerializesJoinsAndCloseRemovesUnsubmittedConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	stateDir := filepath.Join(t.TempDir(), "agent-state")
	options := Options{
		Server: server.URL, StateDir: stateDir,
		TLS: client.TLSOptions{ServerCAFile: writeTestCA(t, server)},
	}
	first, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare first join: %v", err)
	}
	configPath := first.ServiceConfigPath()
	if _, err := Prepare(options); err == nil || !strings.Contains(err.Error(), "another enrollment") {
		t.Fatalf("overlapping prepare = %v, want lock rejection", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close unsubmitted join: %v", err)
	}
	if _, err := os.Lstat(configPath); !os.IsNotExist(err) {
		t.Fatalf("unsubmitted pending config remains after close: %v", err)
	}
	second, err := Prepare(options)
	if err != nil {
		t.Fatalf("prepare after lock release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second unsubmitted join: %v", err)
	}
}
