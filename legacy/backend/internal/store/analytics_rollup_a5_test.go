package store

import (
	"context"
	"testing"
	"time"

	"porter/internal/types"
)

// Hermetic: nil pool must be a safe no-op, never a panic.
func TestA5UpsertAnalyticsDailyNilPool(t *testing.T) {
	s := &Store{}
	s.UpsertAnalyticsDaily("proj-x", time.Now(), 1, 2, 3) // must not panic
}

// Hermetic: nil pool counts zero.
func TestA5DailyTrafficCountsNilPool(t *testing.T) {
	s := &Store{}
	if req, inv := s.DailyTrafficCounts("proj-x", time.Now()); req != 0 || inv != 0 {
		t.Fatalf("nil pool must count (0,0), got (%d,%d)", req, inv)
	}
}

// PG-gated: the rollup is an idempotent rewrite — a second upsert for the
// same (project, day) replaces counters instead of incrementing, and other
// days are untouched.
func TestA5AnalyticsDailyRewritePG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres analytics test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	p := &types.Project{ID: NewID(), Name: "a5-analytics-" + NewID()[:8]}
	s.PutProject(p)
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, p.ID)
	}()
	day := time.Now()
	other := day.Add(24 * time.Hour)

	s.UpsertAnalyticsDaily(p.ID, day, 10, 100, 3)
	s.UpsertAnalyticsDaily(p.ID, day, 12, 200, 4) // rewrite, not increment
	s.UpsertAnalyticsDaily(p.ID, other, 5, 50, 1)

	var req, bw, inv int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT requests, bandwidth, invocations FROM analytics_daily
		 WHERE project_id = $1 AND day = $2::date`,
		p.ID, day.Format("2006-01-02")).Scan(&req, &bw, &inv)
	if err != nil {
		t.Fatalf("read back analytics_daily: %v", err)
	}
	if req != 12 || bw != 200 || inv != 4 {
		t.Fatalf("rollup must rewrite absolute counters, got (%d,%d,%d)", req, bw, inv)
	}
	var oreq int64
	err = s.pool.QueryRow(context.Background(),
		`SELECT requests FROM analytics_daily
		 WHERE project_id = $1 AND day = $2::date`,
		p.ID, other.Format("2006-01-02")).Scan(&oreq)
	if err != nil {
		t.Fatalf("read back other day: %v", err)
	}
	if oreq != 5 {
		t.Fatalf("other day must be independent, got requests=%d", oreq)
	}
}
