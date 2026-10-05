// Durable log + metric sidecar (PostgreSQL as the queryable history next to
// the in-memory rings): per-VM log lines ship exactly once into vm_logs,
// metrics_samples carries the time series, and retention prunes both. The
// rings stay as the hot tail; these tables are the durable truth behind
// log tail APIs, the Prometheus PG gauges, and usage audits.
package store

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"porter/internal/types"
)

// VMLogLine is one durable log row.
type VMLogLine struct {
	ID        int64
	VMID      string
	ProjectID string
	Line      string
	TS        time.Time
}

// uuidOrNil maps VM ids to the UUID columns: real UUIDs pass through,
// short-lived non-UUID ids (ephem-*) land as NULL rather than failing.
func uuidOrNil(s string) any {
	if s == "" {
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		return nil
	}
	return s
}

// AppendVMLog inserts one log line into vm_logs.
func (s *Store) AppendVMLog(vmID, projectID, line string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO vm_logs (vm_id, project_id, line) VALUES ($1, $2, $3)`,
		uuidOrNil(vmID), uuidOrNil(projectID), line)
	if err != nil {
		log.Printf("store: append vm log: %v", err)
	}
	return err
}

// VMLogTail returns the newest lines for vmID, newest-first, with keyset
// pagination (beforeID=0 starts at the head).
func (s *Store) VMLogTail(vmID string, limit int, beforeID int64) []VMLogLine {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, vm_id::text, COALESCE(project_id::text,''), line, ts FROM vm_logs
		 WHERE vm_id::text = $1 AND ($2 = 0 OR id < $2)
		 ORDER BY id DESC LIMIT $3`, vmID, beforeID, limit)
	if err != nil {
		log.Printf("store: vm log tail: %v", err)
		return nil
	}
	defer rows.Close()
	var out []VMLogLine
	for rows.Next() {
		var l VMLogLine
		if err := rows.Scan(&l.ID, &l.VMID, &l.ProjectID, &l.Line, &l.TS); err != nil {
			continue
		}
		out = append(out, l)
	}
	return out
}

// SearchVMLogs returns newest-first lines for vmID matching query (ILIKE,
// literal — wildcards in the query are escaped), capped at 500 rows. Empty
// query returns nil.
func (s *Store) SearchVMLogs(vmID, query string, limit int) []VMLogLine {
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if s.pool == nil {
		return nil
	}
	pattern := "%" + escapeLike(query) + "%"
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, vm_id::text, COALESCE(project_id::text,''), line, ts FROM vm_logs
		 WHERE vm_id::text = $1 AND line ILIKE $2 ESCAPE '\'
		 ORDER BY id DESC LIMIT $3`, vmID, pattern, limit)
	if err != nil {
		log.Printf("store: search vm logs: %v", err)
		return nil
	}
	defer rows.Close()
	var out []VMLogLine
	for rows.Next() {
		var l VMLogLine
		if err := rows.Scan(&l.ID, &l.VMID, &l.ProjectID, &l.Line, &l.TS); err != nil {
			continue
		}
		out = append(out, l)
	}
	return out
}

// escapeLike escapes ILIKE wildcards so the search matches literally.
func escapeLike(q string) string {
	q = strings.ReplaceAll(q, `\`, `\\`)
	q = strings.ReplaceAll(q, `%`, `\%`)
	q = strings.ReplaceAll(q, `_`, `\_`)
	return q
}

// PruneVMLogs deletes log rows older than the cutoff; returns rows removed.
func (s *Store) PruneVMLogs(olderThan time.Time) int64 {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM vm_logs WHERE ts < $1`, olderThan)
	if err != nil {
		log.Printf("store: prune vm logs: %v", err)
		return 0
	}
	return res.RowsAffected()
}

// PruneMetrics deletes metric samples older than the cutoff.
func (s *Store) PruneMetrics(olderThan time.Time) int64 {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM metrics_samples WHERE ts < $1`, olderThan)
	if err != nil {
		log.Printf("store: prune metrics: %v", err)
		return 0
	}
	return res.RowsAffected()
}

// LatestMetrics returns the newest sample per (vm, metric) for live gauges.
func (s *Store) LatestMetrics() []types.MetricSample {
	rows, err := s.pool.Query(context.Background(),
		`SELECT DISTINCT ON (vm_id, metric) vm_id::text, metric, value, ts
		 FROM metrics_samples ORDER BY vm_id, metric, ts DESC LIMIT 5000`)
	if err != nil {
		log.Printf("store: latest metrics: %v", err)
		return nil
	}
	defer rows.Close()
	var out []types.MetricSample
	for rows.Next() {
		var m types.MetricSample
		if err := rows.Scan(&m.VMID, &m.Metric, &m.Value, &m.TS); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}
