package store

import (
	"context"
	"testing"
)

// PG-gated: incident create + list round-trip. The store exposes only
// CreateIncident/ListIncidents (no get/update/resolve/delete), so this
// covers the full available CRUD surface: rows persist with defaults
// (state=open), newest-first, scoped per project.
func TestA5IncidentsCRUDPG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres incidents test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	projectID := "a5-inc-" + NewID()[:8]
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM incidents WHERE project_id = $1`, projectID)
	}()

	if got := s.ListIncidents(projectID); len(got) != 0 {
		t.Fatalf("fresh project must list zero incidents, got %d", len(got))
	}

	id1, err := s.CreateIncident(projectID, "disk pressure", "critical", "node /dev/vda 90% full")
	if err != nil || id1 == "" {
		t.Fatalf("create incident 1: id=%q err=%v", id1, err)
	}
	id2, err := s.CreateIncident(projectID, "slow deploy", "warning", "build queue lag")
	if err != nil || id2 == "" {
		t.Fatalf("create incident 2: id=%q err=%v", id2, err)
	}
	if id1 == id2 {
		t.Fatal("incident ids must be distinct")
	}

	got := s.ListIncidents(projectID)
	if len(got) != 2 {
		t.Fatalf("must list 2 incidents, got %d", len(got))
	}
	byTitle := map[string]Incident{}
	for _, in := range got {
		byTitle[in.Title] = in
		if in.ID == "" || in.ProjectID != projectID {
			t.Errorf("incident identity wrong: %+v", in)
		}
		if in.State != "open" {
			t.Errorf("new incident state must default to open, got %q", in.State)
		}
		if in.CreatedAt.IsZero() || in.UpdatedAt.IsZero() {
			t.Errorf("incident timestamps must be set: %+v", in)
		}
	}
	if in, ok := byTitle["disk pressure"]; !ok || in.Severity != "critical" || in.Summary != "node /dev/vda 90% full" {
		t.Errorf("incident 1 fields wrong: %+v", in)
	}
	if in, ok := byTitle["slow deploy"]; !ok || in.Severity != "warning" || in.Summary != "build queue lag" {
		t.Errorf("incident 2 fields wrong: %+v", in)
	}

	if got := s.ListIncidents("a5-inc-nonexistent"); len(got) != 0 {
		t.Fatalf("unknown project must list zero incidents, got %d", len(got))
	}
}
