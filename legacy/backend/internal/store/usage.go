package store

import (
	"context"
	"log"
)

// RecordUsage appends one write-only meter sample (commerce.md). Best-effort:
// metering must never fail a boot/deploy, so errors are logged, not returned.
// idempotencyKey dedupes replays (unique index, empty = no dedupe).
func (s *Store) RecordUsage(projectID, resourceRef, meter string, quantity float64, unit, idempotencyKey string) {
	if s.pool == nil || projectID == "" || meter == "" {
		return
	}
	if unit == "" {
		unit = "count"
	}
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO usage_events (project_id, resource_ref, meter, quantity, unit, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT DO NOTHING`,
		projectID, resourceRef, meter, quantity, unit, idempotencyKey)
	if err != nil {
		log.Printf("store: record usage %s/%s: %v", projectID, meter, err)
	}
}
