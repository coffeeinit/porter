package controller

import (
	"testing"
	"time"

	"porter/internal/types"
)

// fakePreviewStore is an in-memory PreviewStore double (no PG).
type fakePreviewStore struct {
	projects []*types.Project
	deploys  map[string][]*types.Deployment
	vms      map[string]*types.VM
	envs     map[string][]*types.Environment
	deletedV int
	deletedD int
	deletedE int
}

func (f *fakePreviewStore) ListProjects() []*types.Project { return f.projects }

func (f *fakePreviewStore) ListDeployments(projectID string) []*types.Deployment {
	return f.deploys[projectID]
}

func (f *fakePreviewStore) GetVM(id string) (*types.VM, bool) {
	v, ok := f.vms[id]
	return v, ok
}

func (f *fakePreviewStore) DeleteVM(id string) {
	if _, ok := f.vms[id]; ok {
		delete(f.vms, id)
		f.deletedV++
	}
}

func (f *fakePreviewStore) DeleteDomain(projectID, domain string) { f.deletedD++ }

func (f *fakePreviewStore) ListEnvironments(projectID string) []*types.Environment {
	return f.envs[projectID]
}

func (f *fakePreviewStore) DeleteEnvironment(id string) bool {
	for pid, list := range f.envs {
		for i, e := range list {
			if e != nil && e.ID == id {
				f.envs[pid] = append(list[:i], list[i+1:]...)
				f.deletedE++
				return true
			}
		}
	}
	return false
}

func sweepFixture(now time.Time) (*fakePreviewStore, time.Duration) {
	old := now.Add(-30 * 24 * time.Hour)
	return &fakePreviewStore{
		projects: []*types.Project{{ID: "p1"}},
		deploys: map[string][]*types.Deployment{
			"p1": {{
				ID: "dep-old", ProjectID: "p1", Environment: "preview",
				VMIDs: []string{"vm1", "vm2"}, PreviewURL: "feat.p1.preview.example.com",
				CreatedAt: old,
			}},
		},
		vms: map[string]*types.VM{
			"vm1": {ID: "vm1", HealthStatus: "unhealthy"},
			"vm2": {ID: "vm2", HealthStatus: "stopped"},
		},
		envs: map[string][]*types.Environment{
			"p1": {{ID: "env1", ProjectID: "p1", Branch: "feat",
				URL: "feat.p1.preview.example.com", EnvDomain: "feat.p1.preview.example.com"}},
		},
	}, 7 * 24 * time.Hour
}

func TestSweepPreviewsReapsStale(t *testing.T) {
	now := time.Now()
	fs, ttl := sweepFixture(now)
	swept, err := SweepPreviews(fs, ttl, now)
	if err != nil {
		t.Fatalf("sweep must not error, got %v", err)
	}
	if len(swept) != 1 || swept[0] != "dep-old" {
		t.Fatalf("must sweep dep-old, got %v", swept)
	}
	if len(fs.vms) != 0 {
		t.Fatalf("VM rows must be deleted, left %d", len(fs.vms))
	}
	if fs.deletedD != 1 {
		t.Fatalf("preview domain must be deleted once, got %d", fs.deletedD)
	}
	if len(fs.envs["p1"]) != 0 {
		t.Fatalf("matching preview env must be deleted, left %d", len(fs.envs["p1"]))
	}
}

func TestSweepPreviewsKeepsProduction(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	fs := &fakePreviewStore{
		projects: []*types.Project{{ID: "p1"}},
		deploys: map[string][]*types.Deployment{
			"p1": {
				{ID: "dep-prod", ProjectID: "p1", Environment: "production",
					IsProduction: true, PreviewURL: "prod.example.com", CreatedAt: old},
				{ID: "dep-env-prod", ProjectID: "p1", Environment: "production",
					PreviewURL: "prod2.example.com", CreatedAt: old},
			},
		},
		envs: map[string][]*types.Environment{
			"p1": {{ID: "env-prod", ProjectID: "p1",
				URL: "prod.example.com", EnvDomain: "prod.example.com"}},
		},
	}
	swept, _ := SweepPreviews(fs, time.Hour, now)
	if len(swept) != 0 {
		t.Fatalf("production must never sweep, got %v", swept)
	}
	if fs.deletedD != 0 || fs.deletedE != 0 {
		t.Fatal("production domains/envs must be untouched")
	}
}

func TestSweepPreviewsKeepsYoungAndHealthy(t *testing.T) {
	now := time.Now()
	fs := &fakePreviewStore{
		projects: []*types.Project{{ID: "p1"}},
		deploys: map[string][]*types.Deployment{
			"p1": {
				{ID: "dep-young", ProjectID: "p1", Environment: "preview",
					VMIDs: []string{"dead"}, PreviewURL: "young.example.com",
					CreatedAt: now.Add(-time.Hour)},
				{ID: "dep-healthy", ProjectID: "p1", Environment: "preview",
					VMIDs: []string{"alive"}, PreviewURL: "alive.example.com",
					CreatedAt: now.Add(-30 * 24 * time.Hour)},
			},
		},
		vms: map[string]*types.VM{
			"dead":  {ID: "dead", HealthStatus: "unhealthy"},
			"alive": {ID: "alive", HealthStatus: types.HealthHealthy},
		},
	}
	swept, _ := SweepPreviews(fs, 7*24*time.Hour, now)
	if len(swept) != 0 {
		t.Fatalf("young/healthy previews must be kept, got %v", swept)
	}
	if _, ok := fs.vms["alive"]; !ok {
		t.Fatal("healthy VM row must survive")
	}
}

func TestSweepPreviewsNilSafe(t *testing.T) {
	if _, err := SweepPreviews(nil, time.Hour, time.Now()); err != nil {
		t.Fatalf("nil store must return nil error, got %v", err)
	}
	var r *TaskRunner
	r.maybePreviewSweep(time.Now()) // must not panic
}

// sweepTasks doubles both the ledger (TaskStore) and the preview surface so
// maybePreviewSweep picks up the store via type assertion, like the real
// *store.Store does in production.
type sweepTasks struct {
	*fakeTasks
	*fakePreviewStore
}

func TestSkipPreviewBuildBranches(t *testing.T) {
	cases := []struct {
		msg  string
		skip bool
	}{
		{"fix bug", false},
		{"", false},
		{"[skip ci] docs only", true},
		{"[skip cd] no deploy", true},
		{"[SKIP CI] upper", true},
		{"[Skip Cd] mixed", true},
		{"feat: x [skip ci] trailing", true},
		{"skip ci without brackets", false},
		{"[skip ci][skip cd] both", true},
	}
	for _, c := range cases {
		skip, reason := SkipPreviewBuild(c.msg)
		if skip != c.skip {
			t.Fatalf("SkipPreviewBuild(%q) = %v, want %v", c.msg, skip, c.skip)
		}
		if skip && reason == "" {
			t.Fatalf("SkipPreviewBuild(%q) must explain why", c.msg)
		}
		if !skip && reason != "" {
			t.Fatalf("SkipPreviewBuild(%q) must not set a reason", c.msg)
		}
	}
}

func TestSweepPreviewsKeepsNonPreviewEnvs(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	fs := &fakePreviewStore{
		projects: []*types.Project{{ID: "p1"}},
		deploys: map[string][]*types.Deployment{
			"p1": {
				{ID: "dep-staging", ProjectID: "p1", Environment: "staging",
					PreviewURL: "staging.example.com", CreatedAt: old},
				{ID: "dep-empty", ProjectID: "p1", Environment: "",
					PreviewURL: "empty.example.com", CreatedAt: old},
				{ID: "dep-preview-upper", ProjectID: "p1", Environment: " Preview ",
					VMIDs: []string{"dead"}, PreviewURL: "upper.example.com", CreatedAt: old},
			},
		},
		vms: map[string]*types.VM{
			"dead": {ID: "dead", HealthStatus: "unhealthy"},
		},
	}
	swept, _ := SweepPreviews(fs, time.Hour, now)
	if len(swept) != 1 || swept[0] != "dep-preview-upper" {
		t.Fatalf("only explicit preview sweeps, got %v", swept)
	}
	if fs.deletedD != 1 {
		t.Fatalf("one preview domain deleted, got %d", fs.deletedD)
	}
}

func TestPreviewBareBranches(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"feat.p1.preview.example.com", "feat.p1.preview.example.com"},
		{"http://feat-proj.preview.local", "feat-proj.preview.local"},
		{"https://feat.example.com/some/path", "feat.example.com"},
		{"  https://spaced.example.com/  ", "spaced.example.com"},
	}
	for _, c := range cases {
		if got := previewBare(c.in); got != c.want {
			t.Fatalf("previewBare(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSweepPreviewsNilEntriesSafe(t *testing.T) {
	now := time.Now()
	fs, ttl := sweepFixture(now)
	fs.projects = append(fs.projects, nil)
	fs.deploys["p1"] = append(fs.deploys["p1"], nil)
	fs.envs["p1"] = append(fs.envs["p1"], nil)
	swept, err := SweepPreviews(fs, ttl, now)
	if err != nil {
		t.Fatalf("nil entries must not error, got %v", err)
	}
	if len(swept) != 1 || swept[0] != "dep-old" {
		t.Fatalf("valid stale preview still sweeps, got %v", swept)
	}
}

func TestSweepPreviewsEmptyURLReapsVMsWithoutDomain(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	fs := &fakePreviewStore{
		projects: []*types.Project{{ID: "p1"}},
		deploys: map[string][]*types.Deployment{
			"p1": {{
				ID: "dep-nourl", ProjectID: "p1", Environment: "preview",
				VMIDs: []string{"vm1"}, PreviewURL: "", CreatedAt: old,
			}},
		},
		vms: map[string]*types.VM{
			"vm1": {ID: "vm1", HealthStatus: "unhealthy"},
		},
		envs: map[string][]*types.Environment{
			"p1": {{ID: "env-keep", ProjectID: "p1",
				URL: "other.example.com", EnvDomain: "other.example.com"}},
		},
	}
	swept, _ := SweepPreviews(fs, time.Hour, now)
	if len(swept) != 1 || swept[0] != "dep-nourl" {
		t.Fatalf("url-less stale preview still sweeps VMs, got %v", swept)
	}
	if len(fs.vms) != 0 {
		t.Fatal("VM row must be reaped even without a preview URL")
	}
	if fs.deletedD != 0 {
		t.Fatalf("no domain to delete, got %d", fs.deletedD)
	}
	if len(fs.envs["p1"]) != 1 {
		t.Fatal("unrelated env must survive")
	}
}

func TestPreviewSweepHourlyThrottle(t *testing.T) {
	now := time.Now()
	lastPreviewSweep = time.Time{}
	fs, _ := sweepFixture(now)
	st := &sweepTasks{
		fakeTasks:        &fakeTasks{},
		fakePreviewStore: fs,
	}
	r := NewTaskRunner(st, nil, 0)
	r.maybePreviewSweep(now)
	if len(fs.vms) != 0 {
		t.Fatal("first sweep must reap stale VMs")
	}
	if fs.deletedD != 1 {
		t.Fatalf("first sweep must delete one domain, got %d", fs.deletedD)
	}
	// Isolate: only a fresh stale deployment remains.
	old := now.Add(-30 * 24 * time.Hour)
	fs.deploys["p1"] = []*types.Deployment{{
		ID: "dep-old-2", ProjectID: "p1", Environment: "preview",
		PreviewURL: "feat2.example.com", CreatedAt: old,
	}}
	r.maybePreviewSweep(now.Add(10 * time.Minute))
	if fs.deletedD != 1 {
		t.Fatal("sweep inside the hour must not run again")
	}
	r.maybePreviewSweep(now.Add(2 * time.Hour))
	if fs.deletedD != 2 {
		t.Fatalf("sweep after an hour must run again, domains deleted=%d", fs.deletedD)
	}
}
