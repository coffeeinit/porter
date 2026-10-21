// Durable trace persistence (migration 0029): append-only traces, spans,
// and feedback scores behind the observability.TraceStore interface.
package store

import (
	"context"
	"log"
)

// InsertTrace records one trace row (idempotent by primary key).
func (s *Store) InsertTrace(id, service, operation string) {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO traces (id, service, operation) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO NOTHING`, id, service, operation)
	if err != nil {
		log.Printf("store: insert trace: %v", err)
	}
}

// InsertSpan appends one span row.
func (s *Store) InsertSpan(traceID, spanID, parentID, name string, durationMs int64, errMsg string) {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO spans (trace_id, span_id, parent_id, name, duration_ms, error)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (trace_id, span_id) DO NOTHING`,
		traceID, spanID, parentID, name, durationMs, errMsg)
	if err != nil {
		log.Printf("store: insert span: %v", err)
	}
}

// InsertFeedback appends one feedback score.
func (s *Store) InsertFeedback(traceID, name string, value float64) {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO feedback_scores (trace_id, name, value) VALUES ($1, $2, $3)`,
		traceID, name, value)
	if err != nil {
		log.Printf("store: insert feedback: %v", err)
	}
}

// TraceSpans returns span rows for a trace in insertion order.
func (s *Store) TraceSpans(traceID string) []map[string]any {
	rows, err := s.pool.Query(context.Background(),
		`SELECT span_id, parent_id, name, duration_ms, error FROM spans
		 WHERE trace_id = $1 ORDER BY created_at ASC`, traceID)
	if err != nil {
		log.Printf("store: trace spans: %v", err)
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, parent, name, errMsg string
		var ms int64
		if err := rows.Scan(&id, &parent, &name, &ms, &errMsg); err != nil {
			break
		}
		out = append(out, map[string]any{
			"span_id": id, "parent_id": parent, "name": name,
			"duration_ms": ms, "error": errMsg,
		})
	}
	return out
}
