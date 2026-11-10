package buildservice

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
)

var testBuilderDigest = "sha256:" + "a1" + strings.Repeat("b", 62)
var testArtifactDigest = "sha256:" + "c2" + strings.Repeat("d", 62)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

type fakeRuntime struct {
	trace            *[]string
	availableErr     error
	provisionErr     error
	resolveErr       error
	buildErr         error
	verifyErr        error
	cleanupErr       error
	guest            GuestHandle
	commit           string
	artifact         ArtifactReceipt
	boot             GuestBootReceipt
	cancelBuild      context.CancelFunc
	cleanupContext   error
	cleanupKeep      []string
	buildRequests    []GuestBuildRequest
	builderSpecs     []BuilderGuestSpec
	cancelSilently   bool
	buildHasDeadline bool
}

func (f *fakeRuntime) append(value string) {
	if f.trace != nil {
		*f.trace = append(*f.trace, value)
	}
}
func (f *fakeRuntime) CheckAvailable(_ context.Context, imageDigest string) error {
	f.append("available:" + imageDigest)
	return f.availableErr
}
func (f *fakeRuntime) ProvisionBuilder(_ context.Context, spec BuilderGuestSpec) (GuestHandle, error) {
	f.append("provision:" + spec.ImageDigest)
	f.builderSpecs = append(f.builderSpecs, spec)
	return f.guest, f.provisionErr
}
func (f *fakeRuntime) ResolveSourceRef(_ context.Context, _ GuestHandle, source v1.ReleaseSource) (string, error) {
	f.append("resolve:" + source.Ref)
	return f.commit, f.resolveErr
}
func (f *fakeRuntime) BuildInGuest(ctx context.Context, _ GuestHandle, request GuestBuildRequest) (ArtifactReceipt, error) {
	f.append("build:" + request.ResolvedCommit)
	_, f.buildHasDeadline = ctx.Deadline()
	f.buildRequests = append(f.buildRequests, request)
	if f.cancelBuild != nil {
		f.cancelBuild()
		if !f.cancelSilently {
			return ArtifactReceipt{}, ctx.Err()
		}
	}
	return f.artifact, f.buildErr
}
func (f *fakeRuntime) VerifyGuestArtifact(_ context.Context, _ GuestHandle, artifact ArtifactReceipt) (GuestBootReceipt, error) {
	f.append("verify:" + artifact.Digest)
	return f.boot, f.verifyErr
}
func (f *fakeRuntime) CleanupBuilder(ctx context.Context, _ GuestHandle, keep string) error {
	f.append("cleanup:" + keep)
	f.cleanupContext = ctx.Err()
	f.cleanupKeep = append(f.cleanupKeep, keep)
	return f.cleanupErr
}

type fakeReleaseRepository struct {
	trace       *[]string
	release     v1.Release
	created     int
	lastCommit  string
	lastDigest  string
	readyDigest string
	failure     string
	states      []v1.ReleaseState
	createErr   error
	methodErr   error
}

func (f *fakeReleaseRepository) append(value string) {
	if f.trace != nil {
		*f.trace = append(*f.trace, value)
	}
}
func (f *fakeReleaseRepository) CreateRelease(_ context.Context, serviceID string, request v1.CreateReleaseRequest) (v1.Release, error) {
	f.append("create_release")
	if f.createErr != nil {
		return v1.Release{}, f.createErr
	}
	f.created++
	f.release = v1.Release{ID: "rel_build_test", ServiceID: serviceID, Sequence: 1, State: v1.ReleaseQueued, Source: request.Source, Build: request.Build}
	return f.release, nil
}
func (f *fakeReleaseRepository) GetRelease(_ context.Context, id string) (v1.Release, error) {
	f.append("get_release")
	if f.release.ID != id {
		return v1.Release{}, errors.New("release not found")
	}
	return f.release, nil
}
func (f *fakeReleaseRepository) TransitionRelease(_ context.Context, id string, target v1.ReleaseState) error {
	f.append("state:" + string(target))
	if f.methodErr != nil {
		return f.methodErr
	}
	if f.release.ID != id {
		return errors.New("release not found")
	}
	f.release.State = target
	f.states = append(f.states, target)
	return nil
}
func (f *fakeReleaseRepository) PinReleaseCommit(_ context.Context, id, commit string) error {
	f.append("pin_commit")
	if f.methodErr != nil {
		return f.methodErr
	}
	f.lastCommit = commit
	f.release.ResolvedCommit = commit
	return nil
}
func (f *fakeReleaseRepository) SetReleaseArtifactDigest(_ context.Context, id, digest string) error {
	f.append("set_digest")
	if f.methodErr != nil {
		return f.methodErr
	}
	f.lastDigest = digest
	f.release.ArtifactDigest = digest
	return nil
}
func (f *fakeReleaseRepository) MarkReleaseGuestReady(_ context.Context, id, digest string) error {
	f.append("guest_ready")
	if f.methodErr != nil {
		return f.methodErr
	}
	f.readyDigest = digest
	f.release.GuestReady = true
	return nil
}
func (f *fakeReleaseRepository) SetReleaseFailureMessage(_ context.Context, id, message string) error {
	f.append("failure")
	f.failure = message
	f.release.FailureMessage = message
	return nil
}

func validCreateRequest() v1.CreateReleaseRequest {
	return v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: "https://github.com/acme/service.git", Ref: "main"},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "build/Dockerfile"},
	}
}

func validFakeRuntime(trace *[]string) *fakeRuntime {
	return &fakeRuntime{
		trace: trace, guest: GuestHandle{ID: "builder-1", NodeID: "node-1"}, commit: testCommit,
		artifact: ArtifactReceipt{Digest: testArtifactDigest, SizeBytes: 4096, Format: RootfsArtifactFormat},
		boot:     GuestBootReceipt{ArtifactDigest: testArtifactDigest, Booted: true, GuestInitReady: true},
	}
}

func TestBuildReleaseFailsClosedWhenIsolatedRuntimeUnavailable(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	runtime.availableErr = errors.New("no /dev/kvm; private detail")
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	_, err := service.BuildRelease(context.Background(), "svc_1", validCreateRequest())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v, want ErrUnavailable", err)
	}
	if store.created != 0 || strings.Contains(strings.Join(trace, " "), "provision:") {
		t.Fatalf("unavailable runtime created a release or guest: created=%d trace=%v", store.created, trace)
	}
}

func TestBuildReleaseFailsClosedWithoutGuestImageContract(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, "")
	_, err := service.BuildRelease(context.Background(), "svc_1", validCreateRequest())
	if !errors.Is(err, ErrUnavailable) || store.created != 0 || len(trace) != 0 {
		t.Fatalf("error=%v created=%d trace=%v; want no operation without image contract", err, store.created, trace)
	}
}

func TestBuildReleaseRejectsMaliciousSourceAndPathsBeforeRuntimeOrPersistence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1.CreateReleaseRequest)
	}{
		{"http repository", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "http://github.com/acme/repo.git" }},
		{"credentials", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "https://user:secret@github.com/acme/repo.git" }},
		{"loopback", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "https://127.0.0.1/acme/repo" }},
		{"localhost", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "https://localhost/acme/repo" }},
		{"private host", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "https://git.internal/acme/repo" }},
		{"repository traversal", func(r *v1.CreateReleaseRequest) { r.Source.Repository = "https://github.com/acme/../etc" }},
		{"option-like ref", func(r *v1.CreateReleaseRequest) { r.Source.Ref = "--upload-pack=evil" }},
		{"Git ref traversal", func(r *v1.CreateReleaseRequest) { r.Source.Ref = "feature/../main" }},
		{"absolute Dockerfile", func(r *v1.CreateReleaseRequest) { r.Build.DockerfilePath = "/etc/passwd" }},
		{"Dockerfile traversal", func(r *v1.CreateReleaseRequest) { r.Build.DockerfilePath = "../Dockerfile" }},
		{"Windows path", func(r *v1.CreateReleaseRequest) { r.Build.DockerfilePath = `..\\Dockerfile` }},
		{"unsupported build kind", func(r *v1.CreateReleaseRequest) { r.Build.Kind = "shell" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			trace := []string{}
			runtime := validFakeRuntime(&trace)
			store := &fakeReleaseRepository{trace: &trace}
			request := validCreateRequest()
			test.mutate(&request)
			service := New(runtime, store, testBuilderDigest)
			_, err := service.BuildRelease(context.Background(), "svc_1", request)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v, want ErrInvalidRequest", err)
			}
			if store.created != 0 || len(trace) != 0 {
				t.Fatalf("invalid request reached runtime or store: created=%d trace=%v", store.created, trace)
			}
		})
	}
}

func TestBuildReleaseUsesPinnedCommitAndVerifiedDigestThenCleansBuilder(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	release, err := service.BuildRelease(context.Background(), "svc_1", validCreateRequest())
	if err != nil {
		t.Fatalf("BuildRelease: %v", err)
	}
	if release.State != v1.ReleaseReady || release.ResolvedCommit != testCommit || release.ArtifactDigest != testArtifactDigest || !release.GuestReady {
		t.Fatalf("release=%+v, want guest-ready pinned artifact", release)
	}
	if len(runtime.buildRequests) != 1 || runtime.buildRequests[0].ResolvedCommit != testCommit || runtime.buildRequests[0].Source.Ref != "main" {
		t.Fatalf("build did not receive the pinned commit plus source-selection ref: %+v", runtime.buildRequests)
	}
	if len(runtime.builderSpecs) != 1 || runtime.builderSpecs[0].PlacementPolicy != BuilderDedicatedHostOnly || runtime.builderSpecs[0].VCPUs <= 0 || runtime.builderSpecs[0].MemoryMB <= 0 || runtime.builderSpecs[0].DiskMB <= 0 || runtime.builderSpecs[0].MaxBuildDuration <= 0 {
		t.Fatalf("builder provision did not require bounded dedicated-host isolation: %+v", runtime.builderSpecs)
	}
	if !runtime.buildHasDeadline {
		t.Fatal("guest build request did not receive a bounded context deadline")
	}
	if store.lastCommit != testCommit || store.lastDigest != testArtifactDigest || store.readyDigest != testArtifactDigest {
		t.Fatalf("commit/digest persistence mismatch: commit=%q digest=%q ready=%q", store.lastCommit, store.lastDigest, store.readyDigest)
	}
	wantOrder := []string{"pin_commit", "build:" + testCommit, "verify:" + testArtifactDigest, "set_digest", "cleanup:" + testArtifactDigest, "guest_ready", "state:ready"}
	joined := strings.Join(trace, "|")
	last := -1
	for _, token := range wantOrder {
		next := strings.Index(joined, token)
		if next < 0 || next <= last {
			t.Fatalf("trace=%v does not preserve lifecycle order %q", trace, wantOrder)
		}
		last = next
	}
	if len(runtime.cleanupKeep) != 1 || runtime.cleanupKeep[0] != testArtifactDigest || runtime.cleanupContext != nil {
		t.Fatalf("cleanup did not preserve only the verified digest artifact with a live detached context: keep=%v ctxErr=%v", runtime.cleanupKeep, runtime.cleanupContext)
	}
}

func TestBuildReleaseFailureAlwaysCleansPartialGuestAndNeverMarksReady(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*fakeRuntime)
		wantKeep  string
		wantCalls int
		wantStage string
	}{
		{"partial provision", func(r *fakeRuntime) { r.provisionErr = errors.New("/var/lib/porter/firecracker.sock unavailable") }, "", 1, "provision"},
		{"source resolution", func(r *fakeRuntime) { r.resolveErr = errors.New("git output mentions /tmp/private") }, "", 1, "source resolution"},
		{"build failure", func(r *fakeRuntime) { r.buildErr = errors.New("compiler failed at /guest/tmp/build") }, "", 1, "build"},
		{"malformed artifact digest", func(r *fakeRuntime) { r.artifact.Digest = "sha256:bad" }, "", 1, "artifact validation"},
		{"nonpositive artifact size", func(r *fakeRuntime) { r.artifact.SizeBytes = 0 }, "", 1, "artifact validation"},
		{"untrusted archive format", func(r *fakeRuntime) { r.artifact.Format = "../../tmp/unpacked.tar" }, "", 1, "artifact validation"},
		{"boot not verified", func(r *fakeRuntime) { r.boot.GuestInitReady = false }, "", 1, "guest boot verification"},
		{"boot receipt digest mismatch", func(r *fakeRuntime) { r.boot.ArtifactDigest = "sha256:" + strings.Repeat("e", 64) }, "", 1, "guest boot verification"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			trace := []string{}
			runtime := validFakeRuntime(&trace)
			test.configure(runtime)
			store := &fakeReleaseRepository{trace: &trace}
			service := New(runtime, store, testBuilderDigest)
			release, err := service.BuildRelease(context.Background(), "svc_1", validCreateRequest())
			if !errors.Is(err, ErrBuildFailed) || release.State != v1.ReleaseFailed {
				t.Fatalf("release=%+v error=%v; want failed build", release, err)
			}
			if len(runtime.cleanupKeep) != test.wantCalls || runtime.cleanupKeep[0] != test.wantKeep {
				t.Fatalf("cleanup attempts keep=%v, want %d call(s) preserving %q", runtime.cleanupKeep, test.wantCalls, test.wantKeep)
			}
			if store.readyDigest != "" || release.GuestReady || release.State == v1.ReleaseReady {
				t.Fatalf("failed build became guest-ready: release=%+v readyDigest=%q", release, store.readyDigest)
			}
			if !strings.Contains(store.failure, test.wantStage) || strings.Contains(store.failure, "/") {
				t.Fatalf("failure detail=%q leaks internals or misses stage", store.failure)
			}
		})
	}
}

func TestBuildReleaseCleanupFailureFailsClosedAndRetriesArtifactDiscard(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	runtime.cleanupErr = errors.New("socket /run/firecracker.sock still open")
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	release, err := service.BuildRelease(context.Background(), "svc_1", validCreateRequest())
	if !errors.Is(err, ErrBuildFailed) || release.State != v1.ReleaseFailed {
		t.Fatalf("release=%+v error=%v; want cleanup failure", release, err)
	}
	if !reflect.DeepEqual(runtime.cleanupKeep, []string{testArtifactDigest, ""}) {
		t.Fatalf("cleanup attempts=%q; want preserve then discard", runtime.cleanupKeep)
	}
	if release.GuestReady || store.failure != "isolated builder failed during cleanup" {
		t.Fatalf("cleanup failure leaked internal details or marked ready: release=%+v failure=%q", release, store.failure)
	}
}

func TestBuildReleaseCancellationUsesDetachedCleanupContext(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	ctx, cancel := context.WithCancel(context.Background())
	runtime.cancelBuild = cancel
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	release, err := service.BuildRelease(ctx, "svc_1", validCreateRequest())
	if !errors.Is(err, ErrBuildFailed) || release.State != v1.ReleaseFailed {
		t.Fatalf("release=%+v error=%v; want cancelled build failure", release, err)
	}
	if len(runtime.cleanupKeep) != 1 || runtime.cleanupContext != nil {
		t.Fatalf("cancelled build cleanup=%v ctxErr=%v; want cleanup using detached live context", runtime.cleanupKeep, runtime.cleanupContext)
	}
}

func TestValidateComposeSubsetAcceptsOnlyOneSafeBuildService(t *testing.T) {
	doc := []byte("version: '3.9'\nservices:\n  api:\n    build:\n      context: ./app\n      dockerfile: Dockerfile.prod\n")
	got, err := ValidateComposeSubset(doc, "api")
	if err != nil {
		t.Fatal(err)
	}
	want := ComposeBuild{PrimaryService: "api", Context: "./app", Dockerfile: "Dockerfile.prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestValidateComposeSubsetRejectsMaliciousAndUnsupportedDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"multiple services", "services:\n  api:\n    build: .\n  worker:\n    build: .\n"},
		{"host volume", "services:\n  api:\n    build: .\n    volumes:\n      - /:/host\n"},
		{"privileged", "services:\n  api:\n    build: .\n    privileged: true\n"},
		{"unsupported build args", "services:\n  api:\n    build:\n      context: .\n      args:\n        KEY: value\n"},
		{"context traversal", "services:\n  api:\n    build:\n      context: ../outside\n"},
		{"absolute Dockerfile", "services:\n  api:\n    build:\n      context: .\n      dockerfile: /etc/passwd\n"},
		{"multiple documents", "services:\n  api:\n    build: .\n---\nservices:\n  api:\n    build: .\n"},
		{"YAML alias", "x: &shared ignored\nservices:\n  api:\n    build: .\n"},
		{"duplicate key", "services:\n  api:\n    build: .\n    build: ../outside\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateComposeSubset([]byte(test.doc), "api"); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error=%v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestBuildReleaseChecksCancellationBeforeCreatingRelease(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.BuildRelease(ctx, "svc_1", validCreateRequest())
	if !errors.Is(err, ErrUnavailable) || store.created != 0 {
		t.Fatalf("error=%v created=%d; want fail closed before persistence", err, store.created)
	}
}

func TestCancellationAfterSuccessfulGuestBuildStillCleansAndFailsRelease(t *testing.T) {
	trace := []string{}
	runtime := validFakeRuntime(&trace)
	ctx, cancel := context.WithCancel(context.Background())
	runtime.cancelBuild = cancel
	runtime.cancelSilently = true
	store := &fakeReleaseRepository{trace: &trace}
	service := New(runtime, store, testBuilderDigest)
	release, err := service.BuildRelease(ctx, "svc_1", validCreateRequest())
	if !errors.Is(err, ErrBuildFailed) || release.State != v1.ReleaseFailed || release.GuestReady {
		t.Fatalf("release=%+v error=%v; cancellation after build must fail closed", release, err)
	}
	if !reflect.DeepEqual(runtime.cleanupKeep, []string{""}) || strings.Contains(strings.Join(trace, "|"), "verify:") {
		t.Fatalf("cancellation did not discard the guest before verification: cleanup=%v trace=%v", runtime.cleanupKeep, trace)
	}
}

func TestBuildserviceWireReceiptsContainNoHostPathsOrArchiveBytes(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(GuestHandle{}), reflect.TypeOf(ArtifactReceipt{}), reflect.TypeOf(GuestBootReceipt{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.ToLower(field.Name)
			if strings.Contains(name, "path") || strings.Contains(name, "socket") || strings.Contains(name, "archive") || field.Type.Kind() == reflect.Slice {
				t.Errorf("wire receipt %s contains path/socket/archive or slice field %q", typ.Name(), field.Name)
			}
		}
	}
}

func TestBuildserviceDoesNotImportHostProcessOrContainerRuntimes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "os/exec" || strings.HasPrefix(path, "github.com/docker/") || strings.HasPrefix(path, "github.com/moby/buildkit") || strings.HasPrefix(path, "github.com/containers/podman") {
				t.Errorf("buildservice source imports forbidden host execution/runtime package %q", path)
			}
		}
	}
}
