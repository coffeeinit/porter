package store

import (
	"context"
	"log"
	"time"
)

// This file persists bench.Result summaries (PVE-09) for SLO dashboards.

// BenchRun is one stored benchmark summary (millis for JSON/PG simplicity).
type BenchRun struct {
	ID        string
	Name      string
	MemoryMiB int
	SerialMs  int64
	ShellMs   int64
	AgentMs   int64
	RSSMiB    int64
	CreatedAt time.Time
}

// RecordBenchRun inserts one benchmark summary.
func (s *Store) RecordBenchRun(name string, memMiB int, serial, shell, agent time.Duration, rssMiB int64) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO bench_runs (name, memory_mib, serial_ms, shell_ms, agent_ms, rss_mib)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`,
		name, memMiB, serial.Milliseconds(), shell.Milliseconds(), agent.Milliseconds(), rssMiB).Scan(&id)
	if err != nil {
		log.Printf("store: record bench run: %v", err)
	}
	return id, err
}

// ListBenchRuns returns newest-first summaries for a spec name (empty name
// lists all specs).
func (s *Store) ListBenchRuns(name string, limit int) []BenchRun {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id::text, name, memory_mib, serial_ms, shell_ms, agent_ms,
		rss_mib, created_at FROM bench_runs ORDER BY created_at DESC LIMIT $1`
	args := []any{limit}
	if name != "" {
		q = `SELECT id::text, name, memory_mib, serial_ms, shell_ms, agent_ms,
			rss_mib, created_at FROM bench_runs WHERE name = $1
			ORDER BY created_at DESC LIMIT $2`
		args = []any{name, limit}
	}
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		log.Printf("store: list bench runs: %v", err)
		return nil
	}
	defer rows.Close()
	out := []BenchRun{}
	for rows.Next() {
		var b BenchRun
		if err := rows.Scan(&b.ID, &b.Name, &b.MemoryMiB, &b.SerialMs, &b.ShellMs,
			&b.AgentMs, &b.RSSMiB, &b.CreatedAt); err != nil {
			log.Printf("store: scan bench run: %v", err)
			return out
		}
		out = append(out, b)
	}
	return out
}
