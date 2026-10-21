package store

import (
	"context"
	"testing"
	"time"

	"porter/internal/types"
)

func TestEscapeLike(t *testing.T) {
	if got := escapeLike("100%_x\\y"); got != `100\%\_x\\y` {
		t.Fatalf("wildcards must be escaped, got %q", got)
	}
	if got := escapeLike("plain error"); got != "plain error" {
		t.Fatalf("plain text must pass through, got %q", got)
	}
}

func TestSearchVMLogsEmptyQuery(t *testing.T) {
	s := &Store{}
	if got := s.SearchVMLogs("vm1", "", 10); got != nil {
		t.Fatal("empty query must return nil without touching the DB")
	}
}

// TestAnalyticsDailyRoundTrip exercises the rollup write path; live PG only.
func TestAnalyticsDailyRoundTrip(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres analytics test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	p := &types.Project{ID: NewID(), Name: "analytics-test-" + NewID()[:8]}
	s.PutProject(p)
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, p.ID)
	}()
	day := time.Now()
	s.UpsertAnalyticsDaily(p.ID, day, 10, 0, 3)
	s.UpsertAnalyticsDaily(p.ID, day, 12, 0, 4) // rewrite, not increment
	var req, bw, inv int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT requests, bandwidth, invocations FROM analytics_daily
		 WHERE project_id = $1 AND day = $2::date`,
		p.ID, day.Format("2006-01-02")).Scan(&req, &bw, &inv)
	if err != nil {
		t.Fatalf("read back analytics_daily: %v", err)
	}
	if req != 12 || bw != 0 || inv != 4 {
		t.Fatalf("rollup must rewrite absolute counters, got (%d,%d,%d)", req, bw, inv)
	}
	if gotReq, gotInv := s.DailyTrafficCounts(p.ID, day); gotReq != 0 || gotInv != 0 {
		t.Fatalf("empty traffic_logs must count zero, got (%d,%d)", gotReq, gotInv)
	}
}

// TestSearchVMLogsRoundTrip exercises the ILIKE search; live PG only.
func TestSearchVMLogsRoundTrip(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres log-search test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	vmID, projID := NewID(), NewID()
	_ = s.AppendVMLog(vmID, projID, "boot ok")
	_ = s.AppendVMLog(vmID, projID, "FATAL disk error")
	_ = s.AppendVMLog(vmID, projID, "100% done_disk")
	got := s.SearchVMLogs(vmID, "fatal", 10)
	if len(got) != 1 {
		t.Fatalf("case-insensitive search must match 1 line, got %d", len(got))
	}
	got = s.SearchVMLogs(vmID, "100%", 10)
	if len(got) != 1 {
		t.Fatalf("literal %% must match 1 line, got %d", len(got))
	}
	got = s.SearchVMLogs(vmID, "disk", 1)
	if len(got) != 1 {
		t.Fatalf("limit must cap rows, got %d", len(got))
	}
	_, _ = s.pool.Exec(context.Background(), `DELETE FROM vm_logs WHERE vm_id = $1`, vmID)
}
