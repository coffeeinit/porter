// Package buildservice defines the control-plane contract for source builds that
// execute only inside disposable Firecracker builder guests. It contains no host
// process execution path. The active API uses DisabledBuilder until a capable
// agent and accepted builder guest image are explicitly configured.
package buildservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
)

var (
	ErrUnavailable     = errors.New("isolated Firecracker builder is unavailable")
	ErrInvalidRequest  = errors.New("invalid isolated build request")
	ErrBuildFailed     = errors.New("isolated builder job failed")
	ErrInvalidArtifact = errors.New("isolated builder returned an invalid artifact receipt")
	ErrCleanupFailed   = errors.New("isolated builder cleanup failed")
)

const RootfsArtifactFormat = "porter-rootfs-ext4-v1"

type BuilderPlacementPolicy string

const BuilderDedicatedHostOnly BuilderPlacementPolicy = "dedicated-builder-host-only"

var (
	commitPattern  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	servicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	refPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
)

// BuilderGuestSpec describes a bounded, disposable Firecracker builder VM.
// The image is content-addressed and is resolved by the agent, never by a host
// path supplied by the control plane.
type BuilderGuestSpec struct {
	ReleaseID        string
	ImageDigest      string
	PlacementPolicy  BuilderPlacementPolicy
	VCPUs            int
	MemoryMB         int
	DiskMB           int
	MaxBuildDuration time.Duration
}

// GuestHandle is an opaque agent-local identity. It deliberately has no socket
// or filesystem-path fields.
type GuestHandle struct {
	ID     string
	NodeID string
}

// GuestBuildRequest is the only build intent passed to the guest runtime. The
// checkout must use ResolvedCommit, not Source.Ref. Source archives, shell
// commands, host paths, host mounts, and raw build logs are not represented.
type GuestBuildRequest struct {
	ReleaseID      string
	Source         v1.ReleaseSource
	ResolvedCommit string
	Build          v1.BuildSpec
}

// ArtifactReceipt contains only a digest and bounded metadata. Local artifact
// paths are intentionally absent from all control-plane types.
type ArtifactReceipt struct {
	Digest    string
	SizeBytes int64
	Format    string
}

// GuestBootReceipt proves the output artifact was actually booted by the
// agent's local Firecracker runtime and reached the required guest-init state.
type GuestBootReceipt struct {
	ArtifactDigest string
	Booted         bool
	GuestInitReady bool
}

// GuestRuntime is a typed RPC boundary expected to be implemented by the
// authenticated v2 agent. Implementations must select a dedicated builder-only
// host (never a control-plane or production app host), provision/control the VM
// solely through the agent host's local Firecracker Unix socket, and run Git
// and guest build steps only inside the disposable guest. The guest must safely
// handle hostile repository trees/archives (including traversal and symlinks),
// use bounded disk/time/resources and restricted networking, and return only
// path-free receipts. No arbitrary shell command crosses this interface.
type GuestRuntime interface {
	CheckAvailable(ctx context.Context, builderImageDigest string) error
	ProvisionBuilder(ctx context.Context, spec BuilderGuestSpec) (GuestHandle, error)
	ResolveSourceRef(ctx context.Context, guest GuestHandle, source v1.ReleaseSource) (string, error)
	BuildInGuest(ctx context.Context, guest GuestHandle, request GuestBuildRequest) (ArtifactReceipt, error)
	VerifyGuestArtifact(ctx context.Context, guest GuestHandle, artifact ArtifactReceipt) (GuestBootReceipt, error)
	CleanupBuilder(ctx context.Context, guest GuestHandle, preserveArtifactDigest string) error
}

// ReleaseRepository is the minimal durable release lifecycle needed by the
// coordinator. Health-gate completion remains a separate deployment concern.
type ReleaseRepository interface {
	CreateRelease(ctx context.Context, serviceID string, request v1.CreateReleaseRequest) (v1.Release, error)
	GetRelease(ctx context.Context, id string) (v1.Release, error)
	TransitionRelease(ctx context.Context, id string, target v1.ReleaseState) error
	PinReleaseCommit(ctx context.Context, id, commit string) error
	SetReleaseArtifactDigest(ctx context.Context, id, digest string) error
	MarkReleaseGuestReady(ctx context.Context, id, digest string) error
	SetReleaseFailureMessage(ctx context.Context, id, message string) error
}

type ReleaseBuilder interface {
	BuildRelease(ctx context.Context, serviceID string, request v1.CreateReleaseRequest) (v1.Release, error)
}

// DisabledBuilder is the production default until a KVM-capable, authenticated
// agent and versioned builder guest image contract are available.
type DisabledBuilder struct{}

func (DisabledBuilder) BuildRelease(_ context.Context, _ string, _ v1.CreateReleaseRequest) (v1.Release, error) {
	return v1.Release{}, ErrUnavailable
}

type Service struct {
	Runtime            GuestRuntime
	Releases           ReleaseRepository
	BuilderImageDigest string
	CleanupTimeout     time.Duration
}

func New(runtime GuestRuntime, releases ReleaseRepository, builderImageDigest string) *Service {
	return &Service{Runtime: runtime, Releases: releases, BuilderImageDigest: builderImageDigest, CleanupTimeout: 30 * time.Second}
}

func (s *Service) BuildRelease(ctx context.Context, serviceID string, request v1.CreateReleaseRequest) (v1.Release, error) {
	if err := ValidateRequest(request); err != nil {
		return v1.Release{}, err
	}
	if s == nil || s.Runtime == nil || s.Releases == nil || !validDigest(s.BuilderImageDigest) {
		return v1.Release{}, ErrUnavailable
	}
	if ctx == nil || ctx.Err() != nil {
		return v1.Release{}, ErrUnavailable
	}
	if err := s.Runtime.CheckAvailable(ctx, s.BuilderImageDigest); err != nil {
		return v1.Release{}, ErrUnavailable
	}

	request.Source.Repository = strings.TrimSpace(request.Source.Repository)
	request.Source.Ref = strings.TrimSpace(request.Source.Ref)
	request.Build.DockerfilePath = strings.TrimSpace(request.Build.DockerfilePath)
	request.Build.ComposeFile = strings.TrimSpace(request.Build.ComposeFile)
	request.Build.PrimaryService = strings.TrimSpace(request.Build.PrimaryService)
	release, err := s.Releases.CreateRelease(ctx, serviceID, request)
	if err != nil {
		return v1.Release{}, err
	}
	if err := s.Releases.TransitionRelease(ctx, release.ID, v1.ReleaseBuilding); err != nil {
		return s.fail(ctx, release.ID, GuestHandle{}, "start", err)
	}

	spec := BuilderGuestSpec{
		ReleaseID: release.ID, ImageDigest: s.BuilderImageDigest,
		PlacementPolicy: BuilderDedicatedHostOnly,
		VCPUs:           2, MemoryMB: 2048, DiskMB: 10240, MaxBuildDuration: 20 * time.Minute,
	}
	guest, provisionErr := s.Runtime.ProvisionBuilder(ctx, spec)
	if provisionErr != nil {
		return s.fail(ctx, release.ID, guest, "provision", provisionErr)
	}
	if strings.TrimSpace(guest.ID) == "" || strings.TrimSpace(guest.NodeID) == "" {
		return s.fail(ctx, release.ID, guest, "provision", errors.New("agent returned an incomplete guest identity"))
	}
	if err := ctx.Err(); err != nil {
		return s.fail(ctx, release.ID, guest, "cancellation", err)
	}

	commit, err := s.Runtime.ResolveSourceRef(ctx, guest, release.Source)
	if err != nil {
		return s.fail(ctx, release.ID, guest, "source resolution", err)
	}
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !commitPattern.MatchString(commit) {
		return s.fail(ctx, release.ID, guest, "source resolution", errors.New("agent returned a non-immutable commit identity"))
	}
	if err := ctx.Err(); err != nil {
		return s.fail(ctx, release.ID, guest, "cancellation", err)
	}
	if err := s.Releases.PinReleaseCommit(ctx, release.ID, commit); err != nil {
		return s.fail(ctx, release.ID, guest, "commit pin", err)
	}
	if err := ctx.Err(); err != nil {
		return s.fail(ctx, release.ID, guest, "cancellation", err)
	}

	buildCtx, cancelBuild := context.WithTimeout(ctx, spec.MaxBuildDuration)
	artifact, err := s.Runtime.BuildInGuest(buildCtx, guest, GuestBuildRequest{
		ReleaseID: release.ID, Source: release.Source, ResolvedCommit: commit, Build: request.Build,
	})
	cancelBuild()
	if err != nil {
		return s.fail(ctx, release.ID, guest, "build", err)
	}
	if err := ctx.Err(); err != nil {
		return s.fail(ctx, release.ID, guest, "cancellation", err)
	}
	if !validArtifact(artifact) {
		return s.fail(ctx, release.ID, guest, "artifact validation", ErrInvalidArtifact)
	}
	boot, err := s.Runtime.VerifyGuestArtifact(ctx, guest, artifact)
	if err != nil || !boot.Booted || !boot.GuestInitReady || boot.ArtifactDigest != artifact.Digest {
		return s.fail(ctx, release.ID, guest, "guest boot verification", ErrInvalidArtifact)
	}
	if err := ctx.Err(); err != nil {
		return s.fail(ctx, release.ID, guest, "cancellation", err)
	}
	if err := s.Releases.SetReleaseArtifactDigest(ctx, release.ID, artifact.Digest); err != nil {
		return s.fail(ctx, release.ID, guest, "artifact pin", err)
	}

	if err := s.cleanup(ctx, guest, artifact.Digest); err != nil {
		// A second idempotent cleanup attempt discards the retained artifact if
		// durable cleanup failed; the release will never be marked ready.
		_ = s.cleanup(ctx, guest, "")
		return s.failWithoutGuest(ctx, release.ID, "cleanup", err)
	}
	if err := ctx.Err(); err != nil {
		_ = s.cleanup(ctx, guest, "")
		return s.failWithoutGuest(ctx, release.ID, "cancellation", err)
	}
	if err := s.Releases.MarkReleaseGuestReady(context.Background(), release.ID, artifact.Digest); err != nil {
		_ = s.cleanup(ctx, guest, "")
		return s.failWithoutGuest(ctx, release.ID, "guest-ready persistence", err)
	}
	if err := ctx.Err(); err != nil {
		_ = s.cleanup(ctx, guest, "")
		return s.failWithoutGuest(ctx, release.ID, "cancellation", err)
	}
	if err := s.Releases.TransitionRelease(context.Background(), release.ID, v1.ReleaseReady); err != nil {
		_ = s.cleanup(ctx, guest, "")
		return s.failWithoutGuest(ctx, release.ID, "ready transition", err)
	}
	return s.Releases.GetRelease(context.Background(), release.ID)
}

func (s *Service) fail(ctx context.Context, releaseID string, guest GuestHandle, stage string, cause error) (v1.Release, error) {
	cleanupErr := s.cleanup(ctx, guest, "")
	if cleanupErr != nil {
		cause = errors.Join(cause, cleanupErr)
		stage = "cleanup after " + stage
	}
	return s.failWithoutGuest(ctx, releaseID, stage, cause)
}

func (s *Service) failWithoutGuest(_ context.Context, releaseID, stage string, _ error) (v1.Release, error) {
	// Do not persist subprocess output, source contents, local paths, or socket
	// names. The durable status exposes only a bounded stage label.
	message := "isolated builder failed during " + stage
	_ = s.Releases.SetReleaseFailureMessage(context.Background(), releaseID, message)
	_ = s.Releases.TransitionRelease(context.Background(), releaseID, v1.ReleaseFailed)
	release, _ := s.Releases.GetRelease(context.Background(), releaseID)
	return release, fmt.Errorf("%w: %s", ErrBuildFailed, stage)
}

func (s *Service) cleanup(_ context.Context, guest GuestHandle, preserveDigest string) error {
	if s == nil || s.Runtime == nil || strings.TrimSpace(guest.ID) == "" {
		return nil
	}
	timeout := s.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.Runtime.CleanupBuilder(ctx, guest, preserveDigest); err != nil {
		return ErrCleanupFailed
	}
	return nil
}

func ValidateRequest(request v1.CreateReleaseRequest) error {
	if request.Intent != "" && request.Intent != v1.ReleaseIntentPreview && request.Intent != v1.ReleaseIntentProduction {
		return fmt.Errorf("%w: release intent is unsupported", ErrInvalidRequest)
	}
	if request.Source.Kind != v1.SourceGit {
		return fmt.Errorf("%w: only Git sources are supported", ErrInvalidRequest)
	}
	if err := validateRepository(request.Source.Repository); err != nil {
		return fmt.Errorf("%w: repository must be an HTTPS URL without credentials or local-network targets", ErrInvalidRequest)
	}
	if !validRequestedRef(strings.TrimSpace(request.Source.Ref)) {
		return fmt.Errorf("%w: source ref is invalid", ErrInvalidRequest)
	}
	switch request.Build.Kind {
	case v1.BuildDockerfile:
		if !validBuildPath(request.Build.DockerfilePath) {
			return fmt.Errorf("%w: dockerfile_path must be a safe relative path", ErrInvalidRequest)
		}
	case v1.BuildCompose:
		if !validBuildPath(request.Build.ComposeFile) || !servicePattern.MatchString(strings.TrimSpace(request.Build.PrimaryService)) {
			return fmt.Errorf("%w: compose_file or primary_service is invalid", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: build kind is unsupported", ErrInvalidRequest)
	}
	return nil
}

func validRepositoryURL(raw string) bool { return validateRepository(raw) == nil }

func validateRepository(raw string) error {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\\r\n\x00") {
		return errors.New("invalid repository URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("unsupported repository URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("repository host is not a public DNS name")
	}
	if port := u.Port(); port != "" && port != "443" {
		return errors.New("repository port is not allowed")
	}
	if !strings.Contains(host, ".") || strings.ContainsAny(host, " %") {
		return errors.New("repository host is not a fully qualified DNS name")
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.Contains(decoded, "\\") || strings.ContainsRune(decoded, 0) {
		return errors.New("repository path is invalid")
	}
	segments := strings.Split(strings.Trim(decoded, "/"), "/")
	if len(segments) < 2 {
		return errors.New("repository path must identify a project")
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("repository path contains traversal")
		}
	}
	return nil
}

func validRequestedRef(ref string) bool {
	if !refPattern.MatchString(ref) || strings.HasPrefix(ref, "-") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func validBuildPath(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, "\\\r\n\x00") || strings.HasPrefix(raw, "/") || path.IsAbs(raw) {
		return false
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for i, part := range strings.Split(raw, "/") {
		if part == "" || part == ".." || part == "." && i != 0 {
			return false
		}
	}
	return true
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validArtifact(artifact ArtifactReceipt) bool {
	return validDigest(artifact.Digest) && artifact.SizeBytes > 0 && artifact.Format == RootfsArtifactFormat
}
