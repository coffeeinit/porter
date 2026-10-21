package store

import (
	"context"
	"strings"
	"testing"
)

// Pure escape table: ILIKE wildcards (%, _, \) must match literally.
func TestA5EscapeLikeTable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain error", "plain error"},
		{"100% done", `100\% done`},
		{"under_score", `under\_score`},
		{`back\slash`, `back\\slash`},
		{"100%_x\\y", `100\%\_x\\y`},
		{"", ""},
	}
	for _, tc := range cases {
		if got := escapeLike(tc.in); got != tc.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Hermetic: nil pool must return nil without touching the DB.
func TestA5SearchVMLogsNilPool(t *testing.T) {
	s := &Store{}
	if got := s.SearchVMLogs("vm1", "error", 10); got != nil {
		t.Fatalf("nil pool must return nil, got %d rows", len(got))
	}
}

// Hermetic: empty query short-circuits before any DB access.
func TestA5SearchVMLogsEmptyQueryHermetic(t *testing.T) {
	s := &Store{}
	if got := s.SearchVMLogs("vm1", "", 10); got != nil {
		t.Fatal("empty query must return nil")
	}
}

// PG-gated: literal ILIKE semantics — wildcards match literally,
// search is case-insensitive, rows are isolated per VM, limit caps.
func TestA5SearchVMLogsLiteralPG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres log-search test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	vmID, otherVM, projID := NewID(), NewID(), NewID()
	lines := []string{
		"FATAL Disk Error",
		"100% complete_disk",
		"under_score test",
		`back\slash path`,
		"unrelated hello",
	}
	for _, l := range lines {
		if err := s.AppendVMLog(vmID, projID, l); err != nil {
			t.Fatalf("append vm log: %v", err)
		}
	}
	if err := s.AppendVMLog(otherVM, projID, "FATAL other vm line"); err != nil {
		t.Fatalf("append other vm log: %v", err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM vm_logs WHERE vm_id = $1`, vmID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM vm_logs WHERE vm_id = $1`, otherVM)
	}()

	if got := s.SearchVMLogs(vmID, "fatal", 10); len(got) != 1 {
		t.Fatalf("case-insensitive search must match 1 line, got %d", len(got))
	}
	if got := s.SearchVMLogs(vmID, "100%", 10); len(got) != 1 || !strings.Contains(got[0].Line, "100%") {
		t.Fatalf("literal %% must match 1 line, got %d", len(got))
	}
	if got := s.SearchVMLogs(vmID, "_", 10); len(got) != 2 {
		t.Fatalf("literal _ must match exactly the 2 underscore lines, got %d", len(got))
	}
	if got := s.SearchVMLogs(vmID, `back\slash`, 10); len(got) != 1 {
		t.Fatalf("literal backslash must match 1 line, got %d", len(got))
	}
	if got := s.SearchVMLogs(vmID, "e", 1); len(got) != 1 {
		t.Fatalf("limit must cap rows, got %d", len(got))
	}
	if got := s.SearchVMLogs(vmID, "nomatch-xyz-123", 10); len(got) != 0 {
		t.Fatalf("no-match query must return empty, got %d", len(got))
	}
}
