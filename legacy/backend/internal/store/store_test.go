package store

import (
	"os"
	"testing"

	"porter/internal/resource"
	"porter/internal/types"
)

// testDSN returns a PostgreSQL DSN from the environment, or "" when no test DB
// is configured. Postgres-backed tests skip when unset.
func testDSN() string { return os.Getenv("PORTER_TEST_DATABASE_URL") }

func TestTrafficRing(t *testing.T) {
	s := &Store{traffic: map[string][]*types.TrafficEntry{}}
	defer s.Close()
	for i := 0; i < trafficRingSize+10; i++ {
		s.AddTraffic("vm1", &types.TrafficEntry{Method: "GET", Path: "/"})
	}
	got := s.ListTraffic("vm1", 0)
	if len(got) != trafficRingSize {
		t.Fatalf("expected ring capped at %d, got %d", trafficRingSize, len(got))
	}
}

func TestLogRing(t *testing.T) {
	s := &Store{logs: map[string][]string{}}
	defer s.Close()
	for i := 0; i < logRingSize+5; i++ {
		s.AppendLog("vm1", "line")
	}
	got := s.TailLogs("vm1", 0)
	if len(got) != logRingSize {
		t.Fatalf("expected log ring capped at %d, got %d", logRingSize, len(got))
	}
}

// TestVMCRUD exercises Postgres-backed persistence; requires a live DB.
func TestVMCRUD(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres CRUD test")
	}
	// replicas.id is a UUID column, so the fixture ID must be a valid UUID.
	const vmID = "01913f5a-7c1e-7000-8000-00000000cace"
	s := NewStore(testDSN())
	defer s.Close()
	// health_status has a CHECK constraint ('healthy','unhealthy','checking');
	// the zero value '' is rejected by the DB.
	vm := &types.VM{ID: vmID, Name: "cache", State: types.StateRunning, HealthStatus: resource.HealthChecking}
	s.PutVM(vm)

	got, ok := s.GetVM(vmID)
	if !ok {
		t.Fatal("expected vm to exist after PutVM")
	}
	if got.Name != "cache" || got.State != types.StateRunning {
		t.Fatalf("unexpected vm: %+v", got)
	}

	// Delta-based: the test DB is shared, so assert this VM is present
	// rather than a globally empty table.
	list := s.ListVMs()
	found := false
	for _, v := range list {
		if v.ID == vmID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected vm %s in ListVMs (%d rows)", vmID, len(list))
	}

	s.DeleteVM(vmID)
	if _, ok := s.GetVM(vmID); ok {
		t.Fatal("expected vm to be deleted")
	}
}
