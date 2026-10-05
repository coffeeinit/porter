package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"porter/internal/kernel"
	"porter/internal/store"
	"porter/internal/workflow"
)

var errT14Boom = errors.New("t14 boom")

type t14ErrHost struct{ err error }

func (h t14ErrHost) RunJob(context.Context, string, []string, int, int, bool) (int, error) {
	return 0, h.errFor()
}

func (h t14ErrHost) errFor() error { return h.err }

type t14FailSnapshotMover struct{}

func (t14FailSnapshotMover) Snapshot(string) (string, string, error) {
	return "", "", errT14Boom
}

func (t14FailSnapshotMover) Copy(string, string) error { return nil }

func (t14FailSnapshotMover) VerifyStart(string, string, string) error { return nil }

type t14FailCopyMover struct{}

func (t14FailCopyMover) Snapshot(vmID string) (string, string, error) {
	return "snap1", "digest1", nil
}

func (t14FailCopyMover) Copy(string, string) error { return errT14Boom }

func (t14FailCopyMover) VerifyStart(string, string, string) error { return nil }

func TestT14TickMigrateInvalidPlanFails(t *testing.T) {
	// Same source/target is rejected by plan validation.
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "m-bad", Kind: "migrate", ResourceID: "vm1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"m-bad": {"source": "n1", "target": "n1"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Mover = stubMover{}
	r.tick(context.Background())
	if ft.states["m-bad"] != workflow.StateFailed {
		t.Fatalf("same-node migrate must fail, got %q", ft.states["m-bad"])
	}
}

func TestT14TickMigrateMissingFieldsFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "m-miss", Kind: "migrate", ResourceID: "vm1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"m-miss": {"source": "", "target": "n2"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Mover = stubMover{}
	r.tick(context.Background())
	if ft.states["m-miss"] != workflow.StateFailed {
		t.Fatalf("fieldless migrate must fail, got %q", ft.states["m-miss"])
	}
}

func TestT14TickMigrateSnapshotErrorFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "m-snap", Kind: "migrate", ResourceID: "vm1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"m-snap": {"source": "n1", "target": "n2"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Mover = t14FailSnapshotMover{}
	r.tick(context.Background())
	if ft.states["m-snap"] != workflow.StateFailed {
		t.Fatalf("snapshot error must fail op, got %q", ft.states["m-snap"])
	}
}

func TestT14TickMigrateCopyErrorFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "m-copy", Kind: "migrate", ResourceID: "vm1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"m-copy": {"source": "n1", "target": "n2"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Mover = t14FailCopyMover{}
	r.tick(context.Background())
	if ft.states["m-copy"] != workflow.StateFailed {
		t.Fatalf("copy error must fail op, got %q", ft.states["m-copy"])
	}
}

func TestT14TickKernelBuildNilBuilderFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "k-nil", Kind: "kernel-build", ResourceID: "6.18.9", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"k-nil": {"version": "6.18.9"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0) // Kernel nil
	r.tick(context.Background())
	if ft.states["k-nil"] != workflow.StateFailed {
		t.Fatalf("nil-builder kernel-build must fail, got %q", ft.states["k-nil"])
	}
}

func TestT14TickKernelBuildBadConfigFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "k-bad", Kind: "kernel-build", ResourceID: "6.18.9", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"k-bad": {"version": "6.18.9"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.Kernel = &kernel.ScriptBuilder{} // missing script+outdir: Validate fails, no exec
	r.tick(context.Background())
	if ft.states["k-bad"] != workflow.StateFailed {
		t.Fatalf("misconfigured builder must fail, got %q", ft.states["k-bad"])
	}
}

func TestT14TickConfigPushBadEnvJSONFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "c-bad", Kind: "config-push", ResourceID: "svc1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"c-bad": {"base_rev": "", "env_json": "{not-json"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["c-bad"] != workflow.StateFailed {
		t.Fatalf("malformed env_json must fail, got %q", ft.states["c-bad"])
	}
}

func TestT14TickConfigPushEmptyEnvFails(t *testing.T) {
	envRaw, _ := json.Marshal(map[string]string{})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "c-empty", Kind: "config-push", ResourceID: "svc1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"c-empty": {"base_rev": "", "env_json": string(envRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if ft.states["c-empty"] != workflow.StateFailed {
		t.Fatalf("empty env push must fail, got %q", ft.states["c-empty"])
	}
}

func TestT14TickUnknownKindSkipped(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "u1", Kind: "deploy", ResourceID: "svc1", State: workflow.StateQueued}},
		payload: map[string]map[string]string{},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, nil, 0)
	r.tick(context.Background())
	if _, done := ft.states["u1"]; done {
		t.Fatalf("unknown kind must not be executed, got %q", ft.states["u1"])
	}
	if ft.ops[0].State != workflow.StateQueued {
		t.Fatalf("unknown kind must stay QUEUED, got %q", ft.ops[0].State)
	}
}

func TestT14TickMixedDispatch(t *testing.T) {
	envRaw, _ := json.Marshal(map[string]string{"A": "1"})
	cmdRaw, _ := json.Marshal([]string{"echo", "hi"})
	ft := &fakeTasks{
		ops: []store.Operation{
			{ID: "mx1", Kind: "config-push", ResourceID: "svc1", State: workflow.StateQueued},
			{ID: "mx2", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued},
			{ID: "mx3", Kind: "deploy", ResourceID: "svc1", State: workflow.StateQueued},
		},
		payload: map[string]map[string]string{
			"mx1": {"base_rev": "", "env_json": string(envRaw)},
			"mx2": {"cmd_json": string(cmdRaw)},
		},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, stubHost{exit: 0}, 0)
	r.tick(context.Background())
	if ft.states["mx1"] != workflow.StateSucceeded {
		t.Fatalf("config-push must succeed, got %q", ft.states["mx1"])
	}
	if ft.states["mx2"] != workflow.StateSucceeded {
		t.Fatalf("ephemeral must succeed, got %q", ft.states["mx2"])
	}
	if _, done := ft.states["mx3"]; done {
		t.Fatalf("unknown kind must be skipped, got %q", ft.states["mx3"])
	}
}

func TestT14TickEphemeralBadCmdFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "e-bad", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"e-bad": {"cmd_json": "{not-json"}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, stubHost{exit: 0}, 0)
	r.tick(context.Background())
	if ft.states["e-bad"] != workflow.StateFailed {
		t.Fatalf("malformed cmd_json must fail, got %q", ft.states["e-bad"])
	}
}

func TestT14TickEphemeralHostErrorFails(t *testing.T) {
	cmdRaw, _ := json.Marshal([]string{"echo", "hi"})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "e-err", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"e-err": {"cmd_json": string(cmdRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, t14ErrHost{err: errT14Boom}, 0)
	r.tick(context.Background())
	if ft.states["e-err"] != workflow.StateFailed {
		t.Fatalf("host error must fail op, got %q", ft.states["e-err"])
	}
}

func TestT14TickEphemeralNonzeroExitSucceeds(t *testing.T) {
	cmdRaw, _ := json.Marshal([]string{"exit", "3"})
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "e-nz", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"e-nz": {"cmd_json": string(cmdRaw)}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, stubHost{exit: 3}, 0)
	r.tick(context.Background())
	if ft.states["e-nz"] != workflow.StateSucceeded {
		t.Fatalf("exit 3 is a valid guest result and must succeed, got %q", ft.states["e-nz"])
	}
}

func TestT14TickEphemeralEmptyCommandFails(t *testing.T) {
	ft := &fakeTasks{
		ops:     []store.Operation{{ID: "e-empty", Kind: "ephemeral", ResourceID: "alpine", State: workflow.StateQueued}},
		payload: map[string]map[string]string{"e-empty": {}},
		states:  map[string]string{},
		configs: map[string]store.ServiceConfig{},
	}
	r := NewTaskRunner(ft, stubHost{exit: 0}, 0)
	r.tick(context.Background())
	if ft.states["e-empty"] != workflow.StateFailed {
		t.Fatalf("empty command must fail validation, got %q", ft.states["e-empty"])
	}
}
