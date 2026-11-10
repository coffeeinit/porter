package protocol

import (
	"strings"
	"testing"
)

func validBuilderSpec() BuilderBuildSpec {
	return BuilderBuildSpec{
		BuildID: "build-123", RepositoryURL: "https://github.com/example/app.git",
		Commit: strings.Repeat("a", 40), ContextSubdir: "services/web",
		DockerfilePath: "Dockerfile", ImageName: "ghcr.io/example/app:build-123",
		Architecture: "amd64", VCPUs: 2, MemoryMB: 2048, DiskMB: 8192,
		TimeoutSeconds: 900,
	}
}

func TestBuilderBuildSpecValidate(t *testing.T) {
	if err := validBuilderSpec().Validate(); err != nil {
		t.Fatalf("valid builder spec rejected: %v", err)
	}
}

func TestBuilderBuildSpecRejectsUnsafeOrUnpinnedInputs(t *testing.T) {
	base := validBuilderSpec()
	tests := []struct {
		name   string
		mutate func(*BuilderBuildSpec)
	}{
		{"non-https source", func(s *BuilderBuildSpec) { s.RepositoryURL = "http://github.com/example/app.git" }},
		{"empty query delimiter", func(s *BuilderBuildSpec) { s.RepositoryURL = "https://github.com/example/app.git?" }},
		{"embedded credentials", func(s *BuilderBuildSpec) { s.RepositoryURL = "https://user:secret@github.com/example/app.git" }},
		{"branch instead of commit", func(s *BuilderBuildSpec) { s.Commit = "main" }},
		{"path traversal context", func(s *BuilderBuildSpec) { s.ContextSubdir = "../outside" }},
		{"absolute Dockerfile", func(s *BuilderBuildSpec) { s.DockerfilePath = "/tmp/Dockerfile" }},
		{"shell metacharacters in image name", func(s *BuilderBuildSpec) { s.ImageName = "ghcr.io/example/app;touch /tmp/pwned" }},
		{"resource ceiling", func(s *BuilderBuildSpec) { s.MemoryMB = 32769 }},
		{"timeout ceiling", func(s *BuilderBuildSpec) { s.TimeoutSeconds = 3601 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := base
			tc.mutate(&got)
			if err := got.Validate(); err == nil {
				t.Fatal("unsafe builder spec was accepted")
			}
		})
	}
}

func TestBuilderBuildResultMustBeDigestAddressedAndMatchRequest(t *testing.T) {
	spec := validBuilderSpec()
	digest := "sha256:" + strings.Repeat("b", 64)
	result := BuilderBuildResult{
		BuildID: spec.BuildID, OCIReference: "ghcr.io/example/app@" + digest,
		Digest: digest, Architecture: "amd64", SizeBytes: 1234,
	}
	if err := result.ValidateFor(spec); err != nil {
		t.Fatalf("valid build result rejected: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*BuilderBuildResult)
	}{
		{"build ID mismatch", func(r *BuilderBuildResult) { r.BuildID = "another-build" }},
		{"architecture mismatch", func(r *BuilderBuildResult) { r.Architecture = "arm64" }},
		{"tag-only reference", func(r *BuilderBuildResult) { r.OCIReference = "ghcr.io/example/app:latest" }},
		{"malformed digest", func(r *BuilderBuildResult) { r.Digest = "sha256:xyz" }},
		{"non-positive size", func(r *BuilderBuildResult) { r.SizeBytes = 0 }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := result
			tc.mutate(&got)
			if err := got.ValidateFor(spec); err == nil {
				t.Fatal("invalid builder result was accepted")
			}
		})
	}
}
