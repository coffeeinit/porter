package auth

import (
	"testing"
)

func TestResolveScopeEmptyIsPlatform(t *testing.T) {
	st, id := ResolveScope("", "")
	if st != "platform" || id != "" {
		t.Fatalf("empty header must resolve to platform scope, got %q/%q", st, id)
	}
}

func TestResolveScopeStarIsPlatform(t *testing.T) {
	st, id := ResolveScope("org", "*")
	if st != "platform" || id != "" {
		t.Fatalf("'*' must resolve to platform scope, got %q/%q", st, id)
	}
}

func TestResolveScopeTenantDefaultsOrg(t *testing.T) {
	st, id := ResolveScope("", "org-1")
	if st != "org" || id != "org-1" {
		t.Fatalf("tenant header must default to org scope, got %q/%q", st, id)
	}
}

func TestResolveScopeExplicitType(t *testing.T) {
	st, id := ResolveScope("project", "proj-9")
	if st != "project" || id != "proj-9" {
		t.Fatalf("explicit scope type must be kept, got %q/%q", st, id)
	}
}
