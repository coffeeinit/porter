// Task runner: the executors for QUEUED durable intents (OCM-15, PVE-08).
// config-push intents are applied here (base-rev re-validated against live
// service_config, env persisted by compare-and-swap); ephemeral jobs drive
// the EphemeralRun phase machine against an injected host. Anything the
// runner cannot honestly execute fails the op with a reason — never fake.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"porter/internal/cron"
	"porter/internal/deployment"
	"porter/internal/kernel"
	"porter/internal/migrate"
	"porter/internal/observability"
	"porter/internal/secretbox"
	"porter/internal/store"
	"porter/internal/types"
	"porter/internal/workflow"
)

// TaskStore is the ledger surface the runner needs.
type TaskStore interface {
	ActiveOperations() []store.Operation
	ClaimQueuedOps(kinds []string, worker string, limit int) []store.Operation
	SetOperationState(id, state, errMsg string) error
	OperationPayload(id string) map[string]string
	GetServiceConfig(serviceID string) (store.ServiceConfig, error)
	ApplyServiceConfig(serviceID, baseRev string, env map[string]string) (string, error)
}

// EphemeralHost provisions one one-shot run: template/boot, guest exec,
// destroy. The KVMHost below gates on /dev/kvm; node provisioning replaces
// it with a real provisioner without touching the runner.
type EphemeralHost interface {
	RunJob(ctx context.Context, image string, cmd []string, memMiB, cores int, noNet bool) (int, error)
}

// BackupSchedStore is the schedule + snapshot surface the backup scan needs.
// The concrete *store.Store satisfies it; nil disables backup scans.
type BackupSchedStore interface {
	ListProjects() []*types.Project
	ListBackupSchedules(projectID string, enabledOnly bool) []store.BackupSchedule
	MarkBackupScheduleRun(id string, at time.Time)
	RecordSnapshot(vmID, snapType, stateObj, memObj string, sizeBytes int64) (string, error)
	PruneSnapshots(vmID string, keep int) int64
}

// AnalyticsStore is the rollup surface the hourly analytics rollup needs.
// The concrete *store.Store satisfies it; nil disables the rollup.
type AnalyticsStore interface {
	ListProjects() []*types.Project
	DailyTrafficCounts(projectID string, day time.Time) (requests, invocations int64)
	UpsertAnalyticsDaily(projectID string, day time.Time, requests, bandwidth, invocations int64)
}

// TaskRunner polls the ledger and executes QUEUED config-push, ephemeral,
// migrate, and kernel-build ops.
type TaskRunner struct {
	tasks    TaskStore
	host     EphemeralHost
	interval time.Duration
	// Mover executes cold migrations; nil fails migrate ops honestly.
	Mover migrate.Mover
	// Kernel builds kernels via build.sh; nil fails kernel-build ops honestly.
	Kernel *kernel.ScriptBuilder
	// Tracer records one trace per executed op; nil skips tracing.
	Tracer observability.TraceStore
	// SnapshotVM snapshots a workload VM for scheduled backups; nil skips
	// backup scans honestly (schedules stay pending until wiring lands).
	SnapshotVM func(ctx context.Context, vmID string) (stateObj, memObj string, sizeBytes int64, err error)
	// Backups is the schedule/snapshot surface; nil disables backup scans.
	Backups BackupSchedStore
	// Analytics is the rollup surface; nil disables the hourly rollup.
	Analytics AnalyticsStore
	// lastBackupScan throttles backup scans to once per 60s.
	lastBackupScan time.Time
	// lastRollup throttles the analytics rollup to once per hour.
	lastRollup time.Time
	stop       chan struct{}
	stopped    chan struct{}
}

// NewTaskRunner wires the runner; host may be nil (ephemeral then fails
// with an explicit reason instead of hanging QUEUED).
func NewTaskRunner(tasks TaskStore, host EphemeralHost, interval time.Duration) *TaskRunner {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &TaskRunner{tasks: tasks, host: host, interval: interval,
		stop: make(chan struct{}), stopped: make(chan struct{})}
}

// Start begins the poll loop; Stop halts it.
func (r *TaskRunner) Start(ctx context.Context) {
	go func() {
		defer close(r.stopped)
		t := time.NewTicker(r.interval)
		defer t.Stop()
		r.tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			case <-t.C:
				r.tick(ctx)
			}
		}
	}()
}

// Stop halts the loop.
func (r *TaskRunner) Stop() {
	close(r.stop)
	<-r.stopped
}

// runnerKinds are the op kinds this runner owns (claimed, never scanned).
var runnerKinds = []string{"config-push", "ephemeral", "migrate", "kernel-build"}

// workerID identifies this runner in task claims (hostname-pid, stable per
// process so stuck leases are attributable).
func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "runner"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// tick claims owned QUEUED ops (SKIP LOCKED: N runners never double-run)
// and executes each; unknown kinds belong to other runners. The backup scan
// and analytics rollup ride the same tick (each self-throttled) so no extra
// goroutine is needed.
func (r *TaskRunner) tick(ctx context.Context) {
	now := time.Now()
	r.maybeBackupScan(ctx, now)
	r.maybeRollup(now)
	r.maybePreviewSweep(now)
	claimed := r.tasks.ClaimQueuedOps(runnerKinds, workerID(), 50)
	if len(claimed) == 0 {
		return
	}
	for _, op := range claimed {
		start := time.Now()
		var execErr error
		switch op.Kind {
		case "config-push":
			execErr = r.applyConfigPush(op)
		case "ephemeral":
			execErr = r.runEphemeral(ctx, op)
		case "migrate":
			execErr = r.runMigrate(op)
		case "kernel-build":
			execErr = r.runKernelBuild(ctx, op)
		default:
			continue // unknown kinds belong to other runners
		}
		r.trace(op, time.Since(start), execErr)
	}
}

// trace records one span per executed op (nil-safe).
func (r *TaskRunner) trace(op store.Operation, d time.Duration, execErr error) {
	if r.Tracer == nil {
		return
	}
	_ = r.Tracer.PutTrace(observability.Trace{ID: op.ID, Service: "porter", Operation: "task." + op.Kind})
	msg := ""
	if execErr != nil {
		msg = execErr.Error()
	}
	_ = r.Tracer.PutSpan(observability.Span{
		TraceID: op.ID, SpanID: op.ID + ":run", Name: op.Kind,
		DurationM: d.Milliseconds(), Error: msg,
	})
}

// shouldFireBackup reports whether a schedule is due now: the cron matches
// this minute and the last run is nil (never ran) or at least a minute old,
// so a due schedule fires once per matching minute, never twice.
func shouldFireBackup(cronExpr string, lastRun *time.Time, now time.Time) bool {
	if !cron.Matches(cronExpr, now) {
		return false
	}
	if lastRun == nil {
		return true
	}
	return now.Sub(*lastRun) >= time.Minute
}

// maybeBackupScan snapshots due backup schedules (at most once per 60s).
// Nil Backups or SnapshotVM skips honestly — schedules stay pending, nothing
// is faked. Failures leave LastRun untouched so the next due minute retries.
func (r *TaskRunner) maybeBackupScan(ctx context.Context, now time.Time) {
	if r.Backups == nil || r.SnapshotVM == nil {
		return
	}
	if !r.lastBackupScan.IsZero() && now.Sub(r.lastBackupScan) < 60*time.Second {
		return
	}
	r.lastBackupScan = now
	for _, p := range r.Backups.ListProjects() {
		if p == nil {
			continue
		}
		for _, sched := range r.Backups.ListBackupSchedules(p.ID, true) {
			if !shouldFireBackup(sched.Cron, sched.LastRun, now) {
				continue
			}
			if sched.Workload == "" {
				continue
			}
			stateObj, memObj, size, err := r.SnapshotVM(ctx, sched.Workload)
			if err != nil {
				continue
			}
			if _, err := r.Backups.RecordSnapshot(sched.Workload, "scheduled", stateObj, memObj, size); err != nil {
				continue
			}
			r.Backups.PruneSnapshots(sched.Workload, sched.Retention)
			r.Backups.MarkBackupScheduleRun(sched.ID, now)
		}
	}
}

// maybeRollup aggregates traffic_logs into analytics_daily (at most once per
// hour). traffic_logs carries no byte/size column, so bandwidth rolls up as 0;
// requests = COUNT(*), invocations = COUNT(*) under /api/% (see analytics.go).
func (r *TaskRunner) maybeRollup(now time.Time) {
	if r.Analytics == nil {
		return
	}
	if !r.lastRollup.IsZero() && now.Sub(r.lastRollup) < time.Hour {
		return
	}
	r.lastRollup = now
	for _, p := range r.Analytics.ListProjects() {
		if p == nil {
			continue
		}
		requests, invocations := r.Analytics.DailyTrafficCounts(p.ID, now)
		r.Analytics.UpsertAnalyticsDaily(p.ID, now, requests, 0, invocations)
	}
}

// runMigrate drives lock → stop → snapshot → copy → verify → start → done.
func (r *TaskRunner) runMigrate(op store.Operation) error {
	payload := r.tasks.OperationPayload(op.ID)
	plan := migrate.Plan{
		VMID:       op.ResourceID,
		SourceNode: payload["source"],
		TargetNode: payload["target"],
	}
	if err := plan.Validate(); err != nil {
		_ = r.tasks.SetOperationState(op.ID, workflow.StateFailed, err.Error())
		return err
	}
	fail := func(err error) error {
		_ = r.tasks.SetOperationState(op.ID, workflow.StateFailed, "migrate: "+err.Error())
		return err
	}
	if r.Mover == nil {
		return fail(fmt.Errorf("no mover on this host (needs ObjectStore + runtime)"))
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateRunning, "")
	advance := func(want string) error {
		if err := plan.Advance(); err != nil {
			return fail(err)
		}
		if plan.Phase != want {
			return fail(fmt.Errorf("phase drift: got %q want %q", plan.Phase, want))
		}
		return nil
	}
	for _, want := range []string{migrate.PhaseLock, migrate.PhaseStop} {
		if err := advance(want); err != nil {
			return err
		}
	}
	if err := advance(migrate.PhaseSnapshot); err != nil {
		return err
	}
	snapID, digest, err := r.Mover.Snapshot(plan.VMID)
	if err != nil {
		return fail(err)
	}
	plan.SnapshotID, plan.Digest = snapID, digest
	if err := advance(migrate.PhaseCopy); err != nil {
		return err
	}
	if err := r.Mover.Copy(snapID, plan.TargetNode); err != nil {
		return fail(err)
	}
	if err := advance(migrate.PhaseVerify); err != nil {
		return err
	}
	if err := advance(migrate.PhaseStart); err != nil {
		return err
	}
	if err := r.Mover.VerifyStart(plan.VMID, plan.TargetNode, digest); err != nil {
		return fail(err)
	}
	if err := advance(migrate.PhaseDone); err != nil {
		return err
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateSucceeded,
		fmt.Sprintf("migrate: %s → %s", plan.SourceNode, plan.TargetNode))
	return nil
}

// runKernelBuild executes one version build via build.sh.
func (r *TaskRunner) runKernelBuild(ctx context.Context, op store.Operation) error {
	fail := func(err error) error {
		_ = r.tasks.SetOperationState(op.ID, workflow.StateFailed, "kernel-build: "+err.Error())
		return err
	}
	if r.Kernel == nil {
		return fail(fmt.Errorf("no builder configured"))
	}
	payload := r.tasks.OperationPayload(op.ID)
	version := payload["version"]
	if version == "" {
		version = op.ResourceID
	}
	mgr := kernel.NewManager()
	build, err := mgr.Start(version)
	if err != nil {
		return fail(err)
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateRunning, "")
	if err := r.Kernel.Run(ctx, build); err != nil {
		return fail(err)
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateSucceeded,
		fmt.Sprintf("kernel-build: %s completed", version))
	return nil
}

// applyConfigPush re-validates the base rev against live state and persists
// the env by compare-and-swap. Stale revs fail with the current rev so the
// editor can re-read and retry.
func (r *TaskRunner) applyConfigPush(op store.Operation) error {
	fail := func(msg string) error {
		_ = r.tasks.SetOperationState(op.ID, workflow.StateFailed, msg)
		return fmt.Errorf("%s", msg)
	}
	payload := r.tasks.OperationPayload(op.ID)
	baseRev := payload["base_rev"]
	var env map[string]string
	if raw := payload["env_json"]; raw != "" {
		env = map[string]string{}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return fail(fmt.Sprintf("config-push: bad env payload: %v", err))
		}
	}
	req := deployment.PushRequest{ServiceID: op.ResourceID, BaseRev: baseRev, Env: env}
	cur, err := r.tasks.GetServiceConfig(op.ResourceID)
	if err != nil {
		return fail(fmt.Sprintf("config-push: read live config: %v", err))
	}
	if err := deployment.ValidatePush(req, cur.Rev); err != nil {
		return fail(err.Error())
	}
	// Resolve exec:porter: refs to their names for the managed block; values
	// resolve server-side at guest boot, never here, never in logs.
	for k, v := range env {
		if _, ok := secretbox.ParseExecRef(v); ok {
			env[k] = v
		}
	}
	newRev, err := r.tasks.ApplyServiceConfig(op.ResourceID, baseRev, env)
	if err != nil {
		return fail(err.Error())
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateSucceeded,
		fmt.Sprintf("config-push: applied rev %s", newRev))
	return nil
}

// runEphemeral drives the run → exec → destroy phase machine. Without a
// host it fails explicitly (needs a KVM provisioner) instead of stranding.
func (r *TaskRunner) runEphemeral(ctx context.Context, op store.Operation) error {
	fail := func(msg string) error {
		_ = r.tasks.SetOperationState(op.ID, workflow.StateFailed, msg)
		return fmt.Errorf("%s", msg)
	}
	if r.host == nil {
		return fail("ephemeral: no executor on this host (needs KVM node provisioner)")
	}
	payload := r.tasks.OperationPayload(op.ID)
	var cmd []string
	if raw := payload["cmd_json"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			return fail(fmt.Sprintf("ephemeral: bad cmd payload: %v", err))
		}
	}
	job := deployment.EphemeralJob{Image: op.ResourceID, Command: cmd}
	if err := job.Validate(); err != nil {
		return fail(err.Error())
	}
	run := workflow.EphemeralRun{JobID: op.ID, Image: job.Image}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateRunning, "")
	if err := run.Advance(nil); err != nil { // prepared -> booted (provision)
		return fail(err.Error())
	}
	exit, err := r.host.RunJob(ctx, job.Image, job.Command, job.MemoryMiB, job.Cores, job.NoNet)
	if err != nil {
		return fail(fmt.Sprintf("ephemeral: host run: %v", err))
	}
	code, err := workflow.ParseExitCode(exit)
	if err != nil {
		return fail(err.Error())
	}
	if err := run.Advance(&code); err != nil { // booted -> executed
		return fail(err.Error())
	}
	if err := run.Advance(nil); err != nil { // executed -> destroyed
		return fail(err.Error())
	}
	_ = r.tasks.SetOperationState(op.ID, workflow.StateSucceeded,
		fmt.Sprintf("ephemeral: exit %d", code))
	return nil
}

// KVMHost gates ephemeral runs on host KVM presence.
type KVMHost struct{}

// HasKVM reports whether /dev/kvm exists on this host.
func HasKVM() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// RunJob refuses without KVM; node provisioning supplies the real boot,
// exec, and destroy against the runtime.
func (KVMHost) RunJob(_ context.Context, _ string, _ []string, _ int, _ int, _ bool) (int, error) {
	if !HasKVM() {
		return 0, fmt.Errorf("no /dev/kvm on this host")
	}
	return 0, fmt.Errorf("ephemeral provisioner pending: template→boot→exec→destroy lands with node provisioning")
}
