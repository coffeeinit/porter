package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"porter/protocol"
)

func TestHeartbeatPreservesMaintenanceState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, state := range []string{"cordoned", "draining"} {
		t.Run(state, func(t *testing.T) {
			nodeID := "node-" + state
			if _, err := s.db.ExecContext(ctx,
				`INSERT INTO nodes(id,name,state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?)`,
				nodeID, nodeID, state, "test-token-hash", now, now); err != nil {
				t.Fatalf("insert node: %v", err)
			}

			err := s.Heartbeat(ctx, protocol.Heartbeat{
				NodeID: nodeID, CPUsFree: 3, MemoryFreeMB: 768, DiskFreeMB: 4096,
				VMCount: 2, At: now,
			})
			if err != nil {
				t.Fatalf("heartbeat: %v", err)
			}

			var gotState string
			var cpusFree, memoryFreeMB, diskFreeMB, vmCount int
			if err := s.db.QueryRowContext(ctx,
				`SELECT state,cpus_free,memory_free_mb,disk_free_mb,vm_count FROM nodes WHERE id=?`, nodeID).
				Scan(&gotState, &cpusFree, &memoryFreeMB, &diskFreeMB, &vmCount); err != nil {
				t.Fatalf("read node: %v", err)
			}
			if gotState != state {
				t.Errorf("heartbeat changed operator state: got %q, want %q", gotState, state)
			}
			if cpusFree != 3 || memoryFreeMB != 768 || diskFreeMB != 4096 || vmCount != 2 {
				t.Errorf("heartbeat capacity not recorded: cpus=%d memory=%d disk=%d vms=%d", cpusFree, memoryFreeMB, diskFreeMB, vmCount)
			}
		})
	}
}

func TestHeartbeatRestoresReadyStateForActiveNode(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes(id,name,state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?)`,
		"node-offline", "node-offline", "offline", "test-token-hash", now, now); err != nil {
		t.Fatalf("insert node: %v", err)
	}

	if err := s.Heartbeat(ctx, protocol.Heartbeat{NodeID: "node-offline", At: now}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM nodes WHERE id=?`, "node-offline").Scan(&state); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if state != "ready" {
		t.Fatalf("heartbeat did not restore active node to ready: got %q", state)
	}
}

func TestSchedulingSkipsNodesWithStaleHeartbeat(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	staleNodeID := "node-stale"
	freshNodeID := "node-fresh"
	for _, node := range []struct {
		id       string
		memoryMB int
		lastSeen time.Time
	}{
		{id: staleNodeID, memoryMB: 8192, lastSeen: now.Add(-nodeHeartbeatFreshness - time.Second)},
		{id: freshNodeID, memoryMB: 1024, lastSeen: now},
	} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO nodes(id,name,cpus,memory_mb,cpus_free,memory_free_mb,state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			node.id, node.id, 4, node.memoryMB, 4, node.memoryMB, "ready", "test-token-hash", node.lastSeen, now)
		if err != nil {
			t.Fatalf("insert node %s: %v", node.id, err)
		}
	}

	project, err := s.CreateProject(ctx, "freshness-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := s.CreateApp(ctx, project.ID, "freshness-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, app.ID, app.Image, 1); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	var gotNodeID, state string
	if err := s.db.QueryRowContext(ctx, `SELECT node_id,state FROM replicas WHERE app_id=?`, app.ID).Scan(&gotNodeID, &state); err != nil {
		t.Fatalf("read replica: %v", err)
	}
	if gotNodeID != freshNodeID || state != "provisioning" {
		t.Fatalf("replica placement=(node %q, state %q), want fresh node %q in provisioning state", gotNodeID, state, freshNodeID)
	}
}

func TestHeartbeatExpiryReconciliationAndRecovery(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, initial := range []string{"ready", "cordoned", "draining"} {
		t.Run(initial, func(t *testing.T) {
			id := "node-" + initial
			maintenance := "active"
			if initial != "ready" {
				maintenance = initial
			}
			_, err := s.db.ExecContext(ctx, `INSERT INTO nodes(id,name,state,maintenance_state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?,?)`, id, id, initial, maintenance, "token", base, base)
			if err != nil {
				t.Fatal(err)
			}
			now := base.Add(nodeHeartbeatFreshness + time.Second)
			changed, err := s.MarkHeartbeatExpiredNodesUnavailable(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if changed != 1 {
				t.Fatalf("changed=%d, want 1", changed)
			}
			var state string
			if err := s.db.QueryRowContext(ctx, `SELECT state,maintenance_state FROM nodes WHERE id=?`, id).Scan(&state, &maintenance); err != nil {
				t.Fatal(err)
			}
			if state != "offline" {
				t.Fatalf("state=%q, want offline", state)
			}
			wantMaintenance := "active"
			if initial != "ready" {
				wantMaintenance = initial
			}
			if maintenance != wantMaintenance {
				t.Fatalf("maintenance=%q, want %q", maintenance, wantMaintenance)
			}
			if err := s.Heartbeat(ctx, protocol.Heartbeat{NodeID: id, At: now}); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT state FROM nodes WHERE id=?`, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			wantState := "ready"
			if initial != "ready" {
				wantState = initial
			}
			if state != wantState {
				t.Fatalf("recovered state=%q, want %q", state, wantState)
			}
		})
	}
}

func TestSetNodeStatePreservesMaintenanceIntentOffline(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	stale := now.Add(-nodeHeartbeatFreshness - time.Second)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes(id,name,state,maintenance_state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?,?)`,
		"node-offline", "node-offline", "offline", "active", "token", stale, stale); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if err := s.SetNodeState(ctx, "node-offline", "cordoned"); err != nil {
		t.Fatalf("cordon offline node: %v", err)
	}
	var state, maintenance string
	if err := s.db.QueryRowContext(ctx, `SELECT state,maintenance_state FROM nodes WHERE id='node-offline'`).Scan(&state, &maintenance); err != nil {
		t.Fatal(err)
	}
	if state != "offline" || maintenance != "cordoned" {
		t.Fatalf("after cordon: state=%q maintenance=%q, want offline/cordoned", state, maintenance)
	}
	if err := s.SetNodeState(ctx, "node-offline", "ready"); err != nil {
		t.Fatalf("uncordon offline node: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT state,maintenance_state FROM nodes WHERE id='node-offline'`).Scan(&state, &maintenance); err != nil {
		t.Fatal(err)
	}
	if state != "offline" || maintenance != "active" {
		t.Fatalf("after uncordon: state=%q maintenance=%q, want offline/active", state, maintenance)
	}
	if err := s.Heartbeat(ctx, protocol.Heartbeat{NodeID: "node-offline", At: now}); err != nil {
		t.Fatalf("recovery heartbeat: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT state,maintenance_state FROM nodes WHERE id='node-offline'`).Scan(&state, &maintenance); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || maintenance != "active" {
		t.Fatalf("after recovery: state=%q maintenance=%q, want ready/active", state, maintenance)
	}
}
