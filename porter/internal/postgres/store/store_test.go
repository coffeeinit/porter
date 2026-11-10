package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
)

func TestNewRequiresDatabase(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) succeeded; want an error")
	}
}

func TestEventCursorValidation(t *testing.T) {
	if err := validateEventCursor(&EventCursor{ID: "evt_a"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero-time event cursor error = %v, want ErrInvalidArgument", err)
	}
	if err := validateEventCursor(&EventCursor{OccurredAt: time.Now().UTC()}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty-ID event cursor error = %v, want ErrInvalidArgument", err)
	}
	if err := validateEventCursor(&EventCursor{OccurredAt: time.Now().UTC(), ID: "evt_a"}); err != nil {
		t.Fatalf("valid event cursor: %v", err)
	}
}

func TestNewIDUsesPrefixAndOpaqueHex(t *testing.T) {
	id, err := newID("prj")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "prj_") {
		t.Fatalf("ID %q does not have the expected prefix", id)
	}
	if len(id) != len("prj_")+24 || !isLowerHex(strings.TrimPrefix(id, "prj_")) {
		t.Fatalf("ID %q is not a 96-bit lowercase-hex identifier", id)
	}
}

func TestNameAndDigestValidation(t *testing.T) {
	if err := validateName("project", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("blank name error = %v, want ErrInvalidArgument", err)
	}
	if err := validateName("project", strings.Repeat("a", 129)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("overlong name error = %v, want ErrInvalidArgument", err)
	}
	if err := validateName("project", "A project"); err != nil {
		t.Fatalf("valid name: %v", err)
	}
	if !validCommit(strings.Repeat("a", 40)) || !validCommit(strings.Repeat("b", 64)) {
		t.Fatal("valid Git object IDs were rejected")
	}
	if validCommit(strings.Repeat("A", 40)) || validCommit(strings.Repeat("a", 39)) {
		t.Fatal("invalid Git object ID was accepted")
	}
	if !validArtifactDigest("sha256:" + strings.Repeat("c", 64)) {
		t.Fatal("valid SHA-256 artifact digest was rejected")
	}
	if validArtifactDigest("sha256:"+strings.Repeat("C", 64)) || validArtifactDigest("sha1:"+strings.Repeat("c", 64)) {
		t.Fatal("invalid artifact digest was accepted")
	}
}

func TestBuildSpecValidation(t *testing.T) {
	for name, build := range map[string]v1.BuildSpec{
		"empty dockerfile path": {Kind: v1.BuildDockerfile},
		"empty compose fields":  {Kind: v1.BuildCompose, ComposeFile: "compose.yaml"},
		"unsupported kind":      {Kind: "container"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateBuildSpec(&build); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("validation error = %v, want ErrInvalidArgument", err)
			}
		})
	}
	build := v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: " Dockerfile "}
	if err := validateBuildSpec(&build); err != nil {
		t.Fatalf("valid Dockerfile build spec: %v", err)
	}
	if build.DockerfilePath != "Dockerfile" {
		t.Fatalf("Dockerfile path was not normalized: %q", build.DockerfilePath)
	}
}

func TestReleaseTransitionRules(t *testing.T) {
	allowed := [][2]v1.ReleaseState{
		{v1.ReleaseQueued, v1.ReleaseBuilding},
		{v1.ReleaseQueued, v1.ReleaseFailed},
		{v1.ReleaseBuilding, v1.ReleaseReady},
		{v1.ReleaseBuilding, v1.ReleaseFailed},
		{v1.ReleaseReady, v1.ReleaseDeploying},
		{v1.ReleaseReady, v1.ReleaseFailed},
		{v1.ReleaseDeploying, v1.ReleaseActive},
		{v1.ReleaseDeploying, v1.ReleaseFailed},
		{v1.ReleaseActive, v1.ReleaseSuperseded},
	}
	for _, transition := range allowed {
		if !canTransitionRelease(transition[0], transition[1]) {
			t.Errorf("transition %q -> %q was rejected", transition[0], transition[1])
		}
	}
	for _, transition := range [][2]v1.ReleaseState{
		{v1.ReleaseQueued, v1.ReleaseActive},
		{v1.ReleaseBuilding, v1.ReleaseDeploying},
		{v1.ReleaseFailed, v1.ReleaseBuilding},
		{v1.ReleaseSuperseded, v1.ReleaseActive},
	} {
		if canTransitionRelease(transition[0], transition[1]) {
			t.Errorf("transition %q -> %q was accepted", transition[0], transition[1])
		}
	}
}

func TestDomainHostnameTXTAndRouteValidation(t *testing.T) {
	hostname, err := normalizeHostname("  API.Example.COM. ")
	if err != nil || hostname != "api.example.com" {
		t.Fatalf("normalized hostname = %q, err=%v", hostname, err)
	}
	for _, invalid := range []string{
		"", "localhost", "127.0.0.1", "https://example.com", "*.example.com",
		"a..example.com", "-bad.example.com", "bad-.example.com", "bad name.example.com",
		strings.Repeat("a", 64) + ".example.com",
	} {
		if _, err := normalizeHostname(invalid); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("normalizeHostname(%q) error = %v, want ErrInvalidArgument", invalid, err)
		}
	}
	challenge, err := newTXTVerificationValue()
	if err != nil || len(challenge) != len("porter-verify=")+64 || !strings.HasPrefix(challenge, "porter-verify=") || !isLowerHex(strings.TrimPrefix(challenge, "porter-verify=")) {
		t.Fatalf("TXT challenge = %q, err=%v", challenge, err)
	}
	for _, valid := range []string{"/", "/api", "/api/v1/", "/foo_bar/~v1"} {
		if err := validateRoutePathPrefix(valid); err != nil {
			t.Errorf("validateRoutePathPrefix(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"//", "//other", "/../admin", "/a//b", "/?x=1", "/%2e%2e", "/bad path"} {
		if err := validateRoutePathPrefix(invalid); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("validateRoutePathPrefix(%q) = %v, want ErrInvalidArgument", invalid, err)
		}
	}
}
