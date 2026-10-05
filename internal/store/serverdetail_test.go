package store

import (
	"context"
	"testing"
	"time"

	"porter/internal/types"
)

// Live-PG tests for the per-server detail surface (WP7). Fixtures use unique
// ids/hostnames and are removed via t.Cleanup (shared database).

// seedServerDetailFixture registers a server, a heartbeat, a project+replica,
// an active placement of that replica on the server, and two metric samples.
func seedServerDetailFixture(t *testing.T, s *Store) (serverID, vmID, projectID string) {
	t.Helper()
	serverID, vmID, projectID = NewID(), NewID(), NewID()
	hostname := "srvdetail-" + NewID()[:8]
	s.PutServer(&types.Server{ID: serverID, Name: hostname, Address: "10.0.0.9:22", Status: "online"})
	if err := s.PutHeartbeat(Heartbeat{
		NodeID: serverID, AgentVersion: "vtest", VCPUFree: 7, MemFreeMiB: 4096, VMCount: 1,
		ReportedAt: time.Now(),
	}); err != nil {
		t.Fatalf("put heartbeat: %v", err)
	}
	s.PutProject(&types.Project{ID: projectID, Name: "srvdetail-" + NewID()[:8]})
	s.PutVM(&types.VM{ID: vmID, Name: "srvdetail-vm", ProjectID: projectID, ServiceName: "web",
		State: types.StateRunning, HealthStatus: "healthy", VCPUs: 2, MemMiB: 512})
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO placements (workload_id, node_id, home_node_id, vm_ip, state)
		 VALUES ($1,$2,$2,'10.42.0.5','active')`, vmID, serverID); err != nil {
		t.Fatalf("seed placement: %v", err)
	}
	now := time.Now()
	for _, m := range []struct {
		metric string
		value  float64
	}{{"cpu_percent", 25}, {"memory_mib", 512}} {
		if err := s.AddMetric(&types.MetricSample{ID: NewID(), VMID: vmID, Metric: m.metric, Value: m.value, TS: now}); err != nil {
			t.Fatalf("seed metric: %v", err)
		}
	}
	s.AppendDaemonLog("server registered: " + hostname + " (10.0.0.9:22)")

	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM placements WHERE node_id = $1`, serverID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM node_heartbeats WHERE node_id = $1`, serverID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM metrics_samples WHERE vm_id = $1`, vmID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM replicas WHERE id = $1`, vmID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM servers WHERE id = $1`, serverID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM daemon_logs WHERE line LIKE '%' || $1 || '%'`, hostname)
	})
	return serverID, vmID, projectID
}

// TestServerDetailBundle covers GetServerDetail: server row, heartbeat free
// capacity, VM counts by state, and allocated resources from the placement.
func TestServerDetailBundle(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres server-detail test")
	}
	s := NewStore(testDSN())
	t.Cleanup(func() { _ = s.Close() }) // registered before the fixture cleanup, so it runs last (LIFO)
	serverID, _, _ := seedServerDetailFixture(t, s)

	d, ok := s.GetServerDetail(serverID)
	if !ok {
		t.Fatal("GetServerDetail must find the registered server")
	}
	if d.Server == nil || d.Server.Address != "10.0.0.9:22" {
		t.Fatalf("server row not threaded: %+v", d.Server)
	}
	if d.Heartbeat == nil {
		t.Fatal("heartbeat row exists and must surface")
	}
	if d.Capacity.VCPUsFree == nil || *d.Capacity.VCPUsFree != 7 {
		t.Fatalf("vcpus_free must come from node_heartbeats, got %+v", d.Capacity.VCPUsFree)
	}
	if d.Capacity.VCPUsTotal != 0 {
		t.Fatalf("fixture server reports no total vcpus, got %d", d.Capacity.VCPUsTotal)
	}
	if d.VMCounts["running"] != 1 || d.VMsPlaced != 1 {
		t.Fatalf("vm counts wrong: %+v (placed=%d)", d.VMCounts, d.VMsPlaced)
	}
	if d.Capacity.VCPUsAllocated != 2 || d.Capacity.MemAllocatedMiB != 512 {
		t.Fatalf("allocated capacity must come from the placed VM, got vcpu=%d mem=%d",
			d.Capacity.VCPUsAllocated, d.Capacity.MemAllocatedMiB)
	}
	if len(d.RecentLogs) == 0 {
		t.Fatal("registration daemon-log line must match the server")
	}

	if _, ok := s.GetServerDetail("no-such-server"); ok {
		t.Fatal("unknown server must return false")
	}
}

// TestServerVMsList covers the placed-VM join with project names and the
// released-placement exclusion.
func TestServerVMsList(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres server-VMs test")
	}
	s := NewStore(testDSN())
	t.Cleanup(func() { _ = s.Close() }) // registered before the fixture cleanup, so it runs last (LIFO)
	serverID, vmID, projectID := seedServerDetailFixture(t, s)

	vms := s.ServerVMsList(serverID, 0)
	if len(vms) != 1 {
		t.Fatalf("expected 1 placed VM, got %d", len(vms))
	}
	v := vms[0]
	if v.ID != vmID || v.ProjectID != projectID {
		t.Fatalf("vm/project ids not threaded: %+v", v)
	}
	if v.ProjectName == "" {
		t.Fatal("project name must be joined in")
	}
	if v.State != types.StateRunning || v.PlacementState != "active" {
		t.Fatalf("states wrong: vm=%q placement=%q", v.State, v.PlacementState)
	}
	if v.VCPUs != 2 || v.MemMiB != 512 {
		t.Fatalf("vm spec wrong: vcpus=%d mem=%d", v.VCPUs, v.MemMiB)
	}
	if got := s.ServerVMsList("no-such-server", 0); len(got) != 0 {
		t.Fatalf("unknown server must list nothing, got %d", len(got))
	}
}

// TestServerMetricsTimeseries covers bucketed aggregation of the placed VM's
// samples, including the released-placement exclusion.
func TestServerMetricsTimeseries(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres server-timeseries test")
	}
	s := NewStore(testDSN())
	t.Cleanup(func() { _ = s.Close() }) // registered before the fixture cleanup, so it runs last (LIFO)
	serverID, vmID, _ := seedServerDetailFixture(t, s)

	to := time.Now().Add(time.Minute)
	from := to.Add(-2 * time.Hour)
	series := s.ServerMetricsTimeseries(serverID, from, to, 60)
	cpu := series["cpu_percent"]
	if len(cpu) != 1 {
		t.Fatalf("expected 1 cpu bucket, got %d (%+v)", len(cpu), series)
	}
	if cpu[0].Avg != 25 || cpu[0].Max != 25 || cpu[0].N != 1 {
		t.Fatalf("cpu bucket wrong: %+v", cpu[0])
	}
	mem := series["memory_mib"]
	if len(mem) != 1 || mem[0].Sum != 512 {
		t.Fatalf("memory bucket wrong: %+v", mem)
	}

	// Release the placement and the series must go empty.
	if err := releasePlacementForTest(s, serverID, vmID); err != nil {
		t.Fatalf("release placement: %v", err)
	}
	series = s.ServerMetricsTimeseries(serverID, from, to, 60)
	if len(series["cpu_percent"]) != 0 || len(series["memory_mib"]) != 0 {
		t.Fatalf("released VMs must drop out of the series: %+v", series)
	}
}

// TestServerDaemonLogsScope covers the best-effort per-server match on
// hostname/id and the limit cap.
func TestServerDaemonLogsScope(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres server-logs test")
	}
	s := NewStore(testDSN())
	t.Cleanup(func() { _ = s.Close() }) // registered before the fixture cleanup, so it runs last (LIFO)
	serverID, _, _ := seedServerDetailFixture(t, s)

	logs := s.ServerDaemonLogs(serverID, 100)
	if len(logs) == 0 {
		t.Fatal("registration line must match by hostname")
	}
	if logs[0].ID < logs[len(logs)-1].ID {
		t.Fatal("logs must be newest-first")
	}
	if got := s.ServerDaemonLogs(serverID, 0); len(got) > 100 {
		t.Fatalf("default limit must be 100, got %d", len(got))
	}
}

// releasePlacementForTest marks the fixture placement released so the
// exclusion paths (state <> 'released') can be exercised.
func releasePlacementForTest(s *Store, serverID, vmID string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE placements SET state='released', released_at=now()
		 WHERE node_id = $1 AND workload_id = $2`, serverID, vmID)
	return err
}
