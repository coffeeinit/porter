package controller

import (
	"context"
	"testing"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// Compile-time: the concrete store satisfies both runner surfaces, so main
// can wire them later without touching the runner.
var (
	_ BackupSchedStore = (*store.Store)(nil)
	_ AnalyticsStore   = (*store.Store)(nil)
)

// fakeSchedStore doubles both BackupSchedStore and AnalyticsStore.
type fakeSchedStore struct {
	projects []*types.Project
	scheds   map[string][]store.BackupSchedule
	marked   []string
	recorded int
	pruned   []string
	req      int64
	inv      int64
	upserts  int
	lastUp   [3]int64
}

func (f *fakeSchedStore) ListProjects() []*types.Project { return f.projects }

func (f *fakeSchedStore) ListBackupSchedules(projectID string, enabledOnly bool) []store.BackupSchedule {
	return f.scheds[projectID]
}

func (f *fakeSchedStore) MarkBackupScheduleRun(id string, at time.Time) {
	f.marked = append(f.marked, id)
	for pid, list := range f.scheds {
		for i := range list {
			if list[i].ID == id {
				cp := at
				list[i].LastRun = &cp
			}
		}
		f.scheds[pid] = list
	}
}

func (f *fakeSchedStore) RecordSnapshot(vmID, snapType, stateObj, memObj string, sizeBytes int64) (string, error) {
	f.recorded++
	return "snap1", nil
}

func (f *fakeSchedStore) PruneSnapshots(vmID string, keep int) int64 {
	f.pruned = append(f.pruned, vmID)
	return 0
}

func (f *fakeSchedStore) DailyTrafficCounts(projectID string, day time.Time) (int64, int64) {
	return f.req, f.inv
}

func (f *fakeSchedStore) UpsertAnalyticsDaily(projectID string, day time.Time, requests, bandwidth, invocations int64) {
	f.upserts++
	f.lastUp = [3]int64{requests, bandwidth, invocations}
}

func TestShouldFireBackup(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 5, 0, 0, time.UTC)
	if !shouldFireBackup("* * * * *", nil, now) {
		t.Fatal("wildcard cron with no last run must fire")
	}
	if shouldFireBackup("0 0 1 1 3", nil, now) {
		t.Fatal("non-matching cron must not fire")
	}
	if shouldFireBackup("not a cron", nil, now) {
		t.Fatal("invalid cron must not fire")
	}
	recent := now.Add(-30 * time.Second)
	if shouldFireBackup("* * * * *", &recent, now) {
		t.Fatal("run 30s ago must not refire within the minute")
	}
	old := now.Add(-2 * time.Minute)
	if !shouldFireBackup("* * * * *", &old, now) {
		t.Fatal("run 2m ago with matching cron must fire")
	}
}

func TestBackupScanFiresDueOnce(t *testing.T) {
	ft := &fakeTasks{ops: nil, payload: map[string]map[string]string{}, states: map[string]string{}, configs: map[string]store.ServiceConfig{}}
	r := NewTaskRunner(ft, nil, 0)
	fs := &fakeSchedStore{
		projects: []*types.Project{{ID: "p1"}},
		scheds: map[string][]store.BackupSchedule{
			"p1": {{ID: "s1", ProjectID: "p1", Workload: "vm1", Cron: "* * * * *", Retention: 3}},
		},
	}
	r.Backups = fs
	r.SnapshotVM = func(ctx context.Context, vmID string) (string, string, int64, error) {
		return "state1", "mem1", 42, nil
	}
	now := time.Now()
	r.maybeBackupScan(context.Background(), now)
	if fs.recorded != 1 || len(fs.marked) != 1 || fs.marked[0] != "s1" {
		t.Fatalf("due schedule must snapshot+mark once, got recorded=%d marked=%v", fs.recorded, fs.marked)
	}
	if len(fs.pruned) != 1 || fs.pruned[0] != "vm1" {
		t.Fatalf("must prune to retention, got %v", fs.pruned)
	}
	// Second scan inside 60s must not refire even though cron still matches.
	r.maybeBackupScan(context.Background(), now.Add(10*time.Second))
	if fs.recorded != 1 {
		t.Fatalf("throttled scan must not resnapshot, got %d", fs.recorded)
	}
}

func TestBackupScanNilSnapshotterSkips(t *testing.T) {
	ft := &fakeTasks{ops: nil, payload: map[string]map[string]string{}, states: map[string]string{}, configs: map[string]store.ServiceConfig{}}
	r := NewTaskRunner(ft, nil, 0)
	fs := &fakeSchedStore{
		projects: []*types.Project{{ID: "p1"}},
		scheds: map[string][]store.BackupSchedule{
			"p1": {{ID: "s1", ProjectID: "p1", Workload: "vm1", Cron: "* * * * *", Retention: 3}},
		},
	}
	r.Backups = fs // SnapshotVM nil: skip honestly, never fake
	r.maybeBackupScan(context.Background(), time.Now())
	if fs.recorded != 0 || len(fs.marked) != 0 {
		t.Fatal("nil SnapshotVM must snapshot nothing")
	}
}

func TestRollupHourlyThrottle(t *testing.T) {
	ft := &fakeTasks{ops: nil, payload: map[string]map[string]string{}, states: map[string]string{}, configs: map[string]store.ServiceConfig{}}
	r := NewTaskRunner(ft, nil, 0)
	fs := &fakeSchedStore{projects: []*types.Project{{ID: "p1"}}, scheds: map[string][]store.BackupSchedule{}, req: 7, inv: 2}
	r.Analytics = fs
	now := time.Now()
	r.maybeRollup(now)
	if fs.upserts != 1 || fs.lastUp != [3]int64{7, 0, 2} {
		t.Fatalf("rollup must upsert (7,0,2), got upserts=%d last=%v", fs.upserts, fs.lastUp)
	}
	r.maybeRollup(now.Add(10 * time.Minute))
	if fs.upserts != 1 {
		t.Fatal("rollup inside the hour must not re-aggregate")
	}
	r.maybeRollup(now.Add(2 * time.Hour))
	if fs.upserts != 2 {
		t.Fatal("rollup after an hour must run again")
	}
}
