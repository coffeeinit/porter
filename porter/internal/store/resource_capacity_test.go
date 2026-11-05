package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"porter/protocol"
)

func TestHeartbeatCapacityReconciliationAndDeleteRelease(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	const nodeID = "node-capacity"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO nodes(id,name,os,arch,cpus,memory_mb,disk_mb,cpus_free,memory_free_mb,disk_free_mb,vm_count,state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nodeID, "capacity-node", "linux", "amd64", 4, 4096, 50000, 4, 4096, 50000, 0, "ready", "test-hash", now, now); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	project, err := s.CreateProject(ctx, "capacity-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := s.CreateApp(ctx, project.ID, "capacity-app", "image:latest", 8080, 2, 1024, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, app.ID, app.Image, 1); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	assertNodeCapacity(t, ctx, s, nodeID, 2, 3072, 1)

	// The current agent reports host totals for CPU and actual MemAvailable.
	// The control plane must preserve VM reservations instead of resetting free
	// capacity to those unadjusted values at every heartbeat.
	if err := s.Heartbeat(ctx, protocol.Heartbeat{NodeID: nodeID, CPUsFree: 4, MemoryFreeMB: 4096, DiskFreeMB: 50000, VMCount: 1}); err != nil {
		t.Fatalf("heartbeat after scheduling: %v", err)
	}
	assertNodeCapacity(t, ctx, s, nodeID, 2, 3072, 1)

	applyCommands, err := s.ClaimCommands(ctx, nodeID)
	if err != nil || len(applyCommands) != 1 {
		t.Fatalf("claim apply command: commands=%+v err=%v", applyCommands, err)
	}
	apply := applyCommands[0]
	if err := s.CompleteCommand(ctx, protocol.CommandResult{CommandID: apply.ID, OperationID: apply.OperationID, NodeID: nodeID, Attempt: apply.Attempt, Success: true, State: "running"}); err != nil {
		t.Fatalf("complete apply command: %v", err)
	}
	if err := s.ScaleApp(ctx, app.ID, 0); err != nil {
		t.Fatalf("scale app to zero: %v", err)
	}
	deleteCommands, err := s.ClaimCommands(ctx, nodeID)
	if err != nil || len(deleteCommands) != 1 || deleteCommands[0].Kind != "vm.delete" {
		t.Fatalf("claim delete command: commands=%+v err=%v", deleteCommands, err)
	}
	remove := deleteCommands[0]
	if err := s.CompleteCommand(ctx, protocol.CommandResult{CommandID: remove.ID, OperationID: remove.OperationID, NodeID: nodeID, Attempt: remove.Attempt, Success: true, State: "deleted"}); err != nil {
		t.Fatalf("complete delete command: %v", err)
	}
	if err := s.Heartbeat(ctx, protocol.Heartbeat{NodeID: nodeID, CPUsFree: 4, MemoryFreeMB: 4096, DiskFreeMB: 50000}); err != nil {
		t.Fatalf("heartbeat after delete: %v", err)
	}
	assertNodeCapacity(t, ctx, s, nodeID, 4, 4096, 0)
}

func assertNodeCapacity(t *testing.T, ctx context.Context, s *Store, nodeID string, cpusFree, memoryFree, vms int) {
	t.Helper()
	var gotCPUs, gotMemory, gotVMs int
	if err := s.db.QueryRowContext(ctx, `SELECT cpus_free,memory_free_mb,vm_count FROM nodes WHERE id=?`, nodeID).Scan(&gotCPUs, &gotMemory, &gotVMs); err != nil {
		t.Fatalf("read node capacity: %v", err)
	}
	if gotCPUs != cpusFree || gotMemory != memoryFree || gotVMs != vms {
		t.Fatalf("node capacity=(%d CPU,%d MB,%d VMs), want (%d CPU,%d MB,%d VMs)", gotCPUs, gotMemory, gotVMs, cpusFree, memoryFree, vms)
	}
}
