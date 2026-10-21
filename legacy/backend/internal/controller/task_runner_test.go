package controller

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"porter/internal/kernel"
	"porter/internal/migrate"
	"porter/internal/observability"
	"porter/internal/store"
	"porter/internal/workflow"
)

type fakeTasks struct {
	ops     []store.Operation
	payload map[string]map[string]string
	states  map[string]string
	configs map[string]store.ServiceConfig
}

func (f *fakeTasks) ActiveOperations() []store.Operation { return f.ops }

// ClaimQueuedOps claims owned QUEUED ops like the ledger (test double moves
// straight to running, mirroring ClaimQueuedOps semantics).
func (f *fakeTasks) ClaimQueuedOps(kinds []string, worker string, limit int) []store.Operation {
	owned := map[string]bool{}
	for _, k := range kinds {
		owned[k] = true
	}
	var out []store.Operation
	for i := range f.ops {
		if f.ops[i].State == workflow.StateQueued && owned[f.ops[i].Kind] && len(out) < limit {
			f.ops[i].State = workflow.StateRunning
			out = append(out, f.ops[i])
		}
	}
	return out
}

func (f *fakeTasks) SetOperationState(id, state, errMsg string) error {
	f.states[id] = state
	for i := range f.ops {
		if f.ops[i].ID == id {
			f.ops[i].State = state
		}
	}
	return nil
}

func (f *fakeTasks) OperationPayload(id string) map[string]string {
	return f.payload[id]
}

func (f *fakeTasks) GetServiceConfig(serviceID string) (store.ServiceConfig, error) {
	if c, ok := f.configs[serviceID]; ok {
		return c, nil
	}
	return store.ServiceConfig{ServiceID: serviceID, Env: map[string]string{}}, nil
}

func (f *fakeTasks) ApplyServiceConfig(serviceID, baseRev string, env map[string]string) (string, error) {
	cur := f.configs[serviceID]
	if cur.Rev != baseRev {
		return cur.Rev, &store.RevConflictError{Current: cur.Rev}
	}
	cur.Rev = "rev2"
	cur.Env = env
	f.configs[serviceID] = cur
	return "rev2", nil
}

func TestConfigPushApplies(t *testing.T) {
	envRaw, _ := json.Marshal(map[string]string{"A": "1"})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op1", Kind: "config-push", ResourceID: "svc1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op1": {"base_rev": "", "env_json": string(envRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["op1"] != workflow.StateSucceeded {
		t.Fatalf("push must succeed, got %q", ft.states["op1"])
	}
	if ft.configs["svc1"].Env["A"] != "1" {
		t.Fatal("env must persist")
	}
}

func TestConfigPushStaleFails(t *testing.T) {
	envRaw, _ := json.Marshal(map[string]string{"A": "1"})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op2", Kind: "config-push", ResourceID: "svc1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op2": {"base_rev": "old", "env_json": string(envRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{"svc1": {ServiceID: "svc1", Rev: "rev2"}},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["op2"] != workflow.StateFailed {
		t.Fatalf("stale push must fail, got %q", ft.states["op2"])
	}
}

type stubHost struct{ exit int }

func (s stubHost) RunJob(context.Context, string, []string, int, int, bool) (int, error) {
	return s.exit, nil
}

func TestEphemeralRuns(t *testing.T) {
	cmdRaw, _ := json.Marshal([]string{"echo", "hi"})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op3", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op3": {"cmd_json": string(cmdRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, stubHost{exit: 0}, 0)
	r.tick(context.Background())
	if ft.states["op3"] != workflow.StateSucceeded {
		t.Fatalf("ephemeral must succeed, got %q", ft.states["op3"])
	}
}

func TestEphemeralNoHostFailsHonest(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op4", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op4": {}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["op4"] != workflow.StateFailed {
		t.Fatalf("hostless ephemeral must fail, got %q", ft.states["op4"])
	}
}

type stubMover struct{}

func (stubMover) Snapshot(vmID string) (string, string, error) {
	return "snap1", migrate.DigestOf([]byte("snap-bytes")), nil
}

func (stubMover) Copy(snapshotID, target string) error { return nil }

func (stubMover) VerifyStart(vmID, target, digest string) error { return nil }

func TestMigrateDrives(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op5", Kind: "migrate", ResourceID: "vm9", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op5": {"source": "n1", "target": "n2"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Mover = stubMover{}
	r.Tracer = observability.NewMemoryTraceStore()
	r.tick(context.Background())
	if ft.states["op5"] != workflow.StateSucceeded {
		t.Fatalf("migrate must succeed, got %q", ft.states["op5"])
	}
	if got := r.Tracer.Spans("op5"); len(got) != 1 || got[0].Error != "" {
		t.Fatalf("trace span must record success: %+v", got)
	}
}

func TestMigrateNoMoverFailsHonest(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op6", Kind: "migrate", ResourceID: "vm9", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op6": {"source": "n1", "target": "n2"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["op6"] != workflow.StateFailed {
		t.Fatalf("moverless migrate must fail, got %q", ft.states["op6"])
	}
}

func TestKernelBuildDrives(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "op7", Kind: "kernel-build", ResourceID: "6.18.9", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"op7": {"version": "6.18.9"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Kernel = &kernel.ScriptBuilder{Script: "build.sh", OutDir: t.TempDir(),
		Command: func(ctx context.Context, _, _, _ string) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "version")
		}}
	r.tick(context.Background())
	if ft.states["op7"] != workflow.StateSucceeded {
		t.Fatalf("kernel-build must succeed, got %q", ft.states["op7"])
	}
}
