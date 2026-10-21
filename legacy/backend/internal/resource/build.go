// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Build is a git-based build that produces an artifact for deployment.
type Build struct {
	Metadata `json:"metadata"`
	Spec     BuildSpec   `json:"spec"`
	Status   BuildStatus `json:"status"`
}

func (b *Build) GetMetadata() *Metadata { return &b.Metadata }
func (b *Build) GetSpec() interface{}   { return b.Spec }
func (b *Build) GetStatus() *Status     { return &b.Status.Status }
func (b *Build) SetStatus(s *Status)    { b.Status.Status = *s }
func (b *Build) GetKind() string        { return KindBuild }

// BuildSpec is the desired state of a build
type BuildSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id"`

	// GitURL is the git repository URL
	GitURL string `json:"git_url"`

	// Branch is the git branch
	Branch string `json:"branch"`

	// Commit is the git commit SHA
	Commit string `json:"commit,omitempty"`

	// Dockerfile path
	Dockerfile string `json:"dockerfile,omitempty"`

	// BuildArgs are build arguments
	BuildArgs map[string]string `json:"build_args,omitempty"`

	// Secrets are build secrets
	Secrets []string `json:"secrets,omitempty"`

	// Target is the build target
	Target string `json:"target,omitempty"`

	// Platform is the target platform
	Platform string `json:"platform,omitempty"`

	// NoCache disables layer caching
	NoCache bool `json:"no_cache,omitempty"`

	// PullBaseImage pulls the base image before build
	PullBaseImage bool `json:"pull_base_image,omitempty"`
}

// BuildStatus is the observed state of a build
type BuildStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"` // pending, running, completed, failed, cancelled

	// Image is the resulting image reference
	Image string `json:"image,omitempty"`

	// ImageDigest is the OCI image digest
	ImageDigest string `json:"image_digest,omitempty"`

	// Log is the build log
	Log string `json:"log,omitempty"`

	// StartedAt is when the build started
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt is when the build completed
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Error is the error message if failed
	Error string `json:"error,omitempty"`

	// SBOM is the Software Bill of Materials
	SBOM string `json:"sbom,omitempty"`

	// Provenance is the build provenance
	Provenance string `json:"provenance,omitempty"`

	// Vulnerabilities is the vulnerability scan result
	Vulnerabilities string `json:"vulnerabilities,omitempty"`
}
