package imagebuilder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectReact(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"dependencies":{"react":"18"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFramework(d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Framework != FrameworkReact {
		t.Fatalf("framework=%s want react", got.Framework)
	}
}

func TestParseCompose(t *testing.T) {
	d := t.TempDir()
	body := "services:\n  web:\n    build: .\n    ports:\n      - \"3000:3000\"\n"
	p := filepath.Join(d, "docker-compose.yml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	svc, err := ParseCompose(p)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "web" {
		t.Fatalf("service=%s", svc.Name)
	}
	if svc.ContextDir != d {
		t.Fatalf("context=%s want %s", svc.ContextDir, d)
	}
}
