// Analytics rollup: traffic_logs (one row per proxied request) folds into
// analytics_daily (one row per project per day) for the historical usage
// endpoints. The rollup is an idempotent rewrite, safe to re-run hourly.
package store

import (
	"context"
	"log"
	"time"
)

// DailyTrafficCounts counts one day of traffic_logs rows for a project:
// requests = COUNT(*), invocations = COUNT(*) served under /api/%. There is
// no bytes column on traffic_logs, so bandwidth has no source — the caller
// records 0 (honest zero, not a measurement).
func (s *Store) DailyTrafficCounts(projectID string, day time.Time) (requests, invocations int64) {
	if s.pool == nil {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dayStr := day.Format("2006-01-02")
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE path LIKE '/api/%')
		FROM traffic_logs
		WHERE project_id = $1 AND ts::date = $2::date`,
		nullableStr(projectID), dayStr).Scan(&requests, &invocations)
	if err != nil {
		log.Printf("store: daily traffic counts: %v", err)
		return 0, 0
	}
	return requests, invocations
}

// UpsertAnalyticsDaily sets the absolute per-day counters for one project.
// ON CONFLICT rewrites (not increments), so the hourly re-roll converges
// instead of double-counting — unlike UpsertDailyAnalytics, which increments
// today's row on the hot path.
func (s *Store) UpsertAnalyticsDaily(projectID string, day time.Time, requests, bandwidth, invocations int64) {
	if s.pool == nil {
		return
	}
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO analytics_daily (project_id, day, requests, bandwidth, invocations)
		VALUES ($1, $2::date, $3, $4, $5)
		ON CONFLICT (project_id, day) DO UPDATE SET
			requests = EXCLUDED.requests,
			bandwidth = EXCLUDED.bandwidth,
			invocations = EXCLUDED.invocations`,
		nullableStr(projectID), day.Format("2006-01-02"), requests, bandwidth, invocations)
	if err != nil {
		log.Printf("store: upsert analytics daily: %v", err)
	}
}
