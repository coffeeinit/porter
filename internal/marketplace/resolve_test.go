package marketplace

import (
	"strings"
	"testing"

	"porter/internal/store"
)

// stubLister is a minimal ServiceTemplateStore for ResolveTemplate tests.
type stubLister struct {
	rows []store.ServiceTemplateRow
}

func (s stubLister) ListServiceTemplates() []store.ServiceTemplateRow {
	return s.rows
}

func TestResolveTemplateDBFirst(t *testing.T) {
	lister := stubLister{rows: []store.ServiceTemplateRow{
		{Name: "postgres", Image: "custom://pg-test", Version: "16",
			Ports: []int{5432}, VolumeMiB: 100,
			Env:         map[string]string{"POSTGRES_DB": "app"},
			Secrets:     []string{"POSTGRES_PASSWORD"},
			Description: "operator postgres"},
	}}
	tmpl, err := ResolveTemplate(lister, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Image != "custom://pg-test" {
		t.Fatalf("expected DB row image, got %q", tmpl.Image)
	}
	if !strings.HasSuffix(tmpl.Description, " (custom)") {
		t.Fatalf("expected custom suffix, got %q", tmpl.Description)
	}
	// Built-in probe reused when no engine_defaults getter is present.
	if tmpl.Healthcheck == nil || tmpl.Healthcheck.Port != 5432 {
		t.Fatalf("expected built-in postgres probe, got %+v", tmpl.Healthcheck)
	}
}

func TestResolveTemplateCaseInsensitive(t *testing.T) {
	lister := stubLister{rows: []store.ServiceTemplateRow{
		{Name: "Redis", Image: "custom://redis-test", Description: "r"},
	}}
	if _, err := ResolveTemplate(lister, "redis"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTemplate(lister, "REDIS"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTemplateBuiltinFallback(t *testing.T) {
	// Nil lister degrades to built-ins.
	tmpl, err := ResolveTemplate(nil, "redis")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Name != "redis" {
		t.Fatalf("expected redis, got %q", tmpl.Name)
	}
	// Empty rows also fall through to built-ins.
	tmpl, err = ResolveTemplate(stubLister{}, "minio")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Image == "" {
		t.Fatal("expected built-in minio image")
	}
	// Unknown engine fails honestly.
	if _, err := ResolveTemplate(stubLister{}, "toaster"); err == nil {
		t.Fatal("unknown template must fail")
	}
	if _, err := ResolveTemplate(nil, "toaster"); err == nil {
		t.Fatal("unknown template must fail with nil lister")
	}
}
