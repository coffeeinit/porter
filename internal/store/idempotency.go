package store

import (
	"context"
	"log"
)

// This file implements idempotency-key storage for mutating writes (task
// T10a). Keys are scoped by the caller (method + route pattern + key) so one
// key never replays across different operations.

// IdempotencyRecord is a stored response for replay.
type IdempotencyRecord struct {
	StatusCode int
	Response   string // raw JSON body
}

// GetIdempotency returns the stored response for a key, if any.
func (s *Store) GetIdempotency(key string) (IdempotencyRecord, bool) {
	var rec IdempotencyRecord
	err := s.pool.QueryRow(context.Background(),
		`SELECT status_code, response::text FROM idempotency_keys WHERE key = $1`, key).
		Scan(&rec.StatusCode, &rec.Response)
	if err != nil {
		return IdempotencyRecord{}, false
	}
	return rec, true
}

// PutIdempotency stores a response for later replay (insert wins; concurrent
// duplicates keep the first stored response).
func (s *Store) PutIdempotency(key string, statusCode int, response string) {
	if response == "" {
		response = "{}"
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO idempotency_keys (key, status_code, response)
		 VALUES ($1, $2, $3::jsonb) ON CONFLICT (key) DO NOTHING`,
		key, statusCode, response)
	if err != nil {
		log.Printf("store: put idempotency key: %v", err)
	}
}
