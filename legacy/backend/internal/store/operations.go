package store

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// This file persists workflow tasks onto the tasks ledger from migration
// 0018 (UUID ids, lowercase status vocabulary, lock_key UNIQUE for
// idempotency). There is exactly one durable-task table: the UPPERCASE
// workflow.Task vocabulary maps to stored lowercase here, and the resource
// scope rides the payload JSONB. (An earlier draft created a second
// operations table; it was removed in favor of this ledger.)

// Operation is one durable task row.
type Operation struct {
	ID             string
	Kind           string
	ResourceKind   string
	ResourceID     string
	State          string // UPPERCASE workflow vocabulary
	Attempt        int
	IdempotencyKey string
	Error          string
	UpdatedAt      time.Time
}

// toStored maps workflow states to the tasks.status vocabulary.
func toStored(state string) string { return strings.ToLower(state) }

// fromStored maps stored status back to workflow vocabulary.
func fromStored(status string) string { return strings.ToUpper(status) }

// CreateOperation inserts a queued task, or returns the existing row id when
// the idempotency key replays (lock_key wins; concurrent duplicates keep the
// first row).
func (s *Store) CreateOperation(kind, resourceKind, resourceID, idemKey string) (string, error) {
	ctx := context.Background()
	if idemKey != "" {
		var existing string
		if err := s.pool.QueryRow(ctx,
			`SELECT id::text FROM tasks WHERE lock_key = $1`, idemKey).Scan(&existing); err == nil {
			return existing, nil
		}
	}
	payload, _ := json.Marshal(map[string]string{
		"resource_kind": resourceKind,
		"resource_id":   resourceID,
	})
	if idemKey != "" {
		var id string
		err := s.pool.QueryRow(ctx,
			`INSERT INTO tasks (kind, payload, status, lock_key)
			 VALUES ($1, $2::jsonb, 'queued', $3)
			 ON CONFLICT (lock_key) DO NOTHING RETURNING id::text`,
			kind, string(payload), idemKey).Scan(&id)
		if err == nil {
			return id, nil
		}
		var winner string
		if err2 := s.pool.QueryRow(ctx,
			`SELECT id::text FROM tasks WHERE lock_key = $1`, idemKey).Scan(&winner); err2 == nil {
			return winner, nil
		}
		log.Printf("store: create operation: %v", err)
		return "", err
	}
	var id string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO tasks (kind, payload, status) VALUES ($1, $2::jsonb, 'queued')
		 RETURNING id::text`, kind, string(payload)).Scan(&id); err != nil {
		log.Printf("store: create operation: %v", err)
		return "", err
	}
	return id, nil
}

// SetOperationState moves an operation; attempts increment on retrying.
func (s *Store) SetOperationState(id, state, errMsg string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE tasks SET status = $2, error = $3, updated_at = now(),
		 attempts = attempts + CASE WHEN $2 = 'retrying' THEN 1 ELSE 0 END
		 WHERE id::text = $1`, id, toStored(state), errMsg)
	if err != nil {
		log.Printf("store: set operation state: %v", err)
	}
	return err
}

// CreateOperationWithPayload inserts a queued task like CreateOperation but
// merges extra fields into the payload JSONB (env maps, base revs, job
// specs) so executors can act without a second lookup. Idempotency behaves
// identically (lock_key wins).
func (s *Store) CreateOperationWithPayload(kind, resourceKind, resourceID, idemKey string, extra map[string]string) (string, error) {
	payloadMap := map[string]string{
		"resource_kind": resourceKind,
		"resource_id":   resourceID,
	}
	for k, v := range extra {
		payloadMap[k] = v
	}
	payload, _ := json.Marshal(payloadMap)
	ctx := context.Background()
	if idemKey != "" {
		var existing string
		if err := s.pool.QueryRow(ctx,
			`SELECT id::text FROM tasks WHERE lock_key = $1`, idemKey).Scan(&existing); err == nil {
			return existing, nil
		}
		var id string
		err := s.pool.QueryRow(ctx,
			`INSERT INTO tasks (kind, payload, status, lock_key)
			 VALUES ($1, $2::jsonb, 'queued', $3)
			 ON CONFLICT (lock_key) DO NOTHING RETURNING id::text`,
			kind, string(payload), idemKey).Scan(&id)
		if err == nil {
			return id, nil
		}
		var winner string
		if err2 := s.pool.QueryRow(ctx,
			`SELECT id::text FROM tasks WHERE lock_key = $1`, idemKey).Scan(&winner); err2 == nil {
			return winner, nil
		}
		log.Printf("store: create operation: %v", err)
		return "", err
	}
	var id string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO tasks (kind, payload, status) VALUES ($1, $2::jsonb, 'queued')
		 RETURNING id::text`, kind, string(payload)).Scan(&id); err != nil {
		log.Printf("store: create operation: %v", err)
		return "", err
	}
	return id, nil
}

// OperationPayload returns the full payload map for one operation.
func (s *Store) OperationPayload(id string) map[string]string {
	var raw string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT payload::text FROM tasks WHERE id::text = $1`, id).Scan(&raw); err != nil {
		log.Printf("store: operation payload: %v", err)
		return map[string]string{}
	}
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ActiveOperations lists non-terminal operations for the reconciler and the// operations API (stuck ones surface via ListStuckOperations).
func (s *Store) ActiveOperations() []Operation {
	return s.listOperations(`SELECT id::text, kind, payload::text, status, attempts,
		COALESCE(lock_key,''), error, updated_at FROM tasks
		WHERE status IN ('queued','running','waiting','retrying')
		ORDER BY updated_at DESC LIMIT 200`)
}

// ListStuckOperations returns operations older than the cutoff that never
// reached a terminal state (host reconciler resets them).
func (s *Store) ListStuckOperations(olderThan time.Time) []Operation {
	return s.listOperations(`SELECT id::text, kind, payload::text, status, attempts,
		COALESCE(lock_key,''), error, updated_at FROM tasks
		WHERE status IN ('queued','running','waiting','retrying') AND updated_at < $1
		ORDER BY updated_at ASC LIMIT 200`, olderThan)
}

func (s *Store) listOperations(q string, args ...any) []Operation {
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		log.Printf("store: list operations: %v", err)
		return nil
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		var o Operation
		var payload, status string
		if err := rows.Scan(&o.ID, &o.Kind, &payload, &status,
			&o.Attempt, &o.IdempotencyKey, &o.Error, &o.UpdatedAt); err != nil {
			log.Printf("store: scan operation: %v", err)
			return out
		}
		var scope map[string]string
		if err := json.Unmarshal([]byte(payload), &scope); err == nil {
			o.ResourceKind, o.ResourceID = scope["resource_kind"], scope["resource_id"]
		}
		o.State = fromStored(status)
		out = append(out, o)
	}
	return out
}

// ClaimQueuedOps atomically claims up to limit QUEUED ops of the given kinds
// for one worker (FOR UPDATE SKIP LOCKED): safe with N runners, no double
// execution. Claimed rows move to running with the worker identity.
func (s *Store) ClaimQueuedOps(kinds []string, worker string, limit int) []Operation {
	if len(kinds) == 0 || limit <= 0 {
		return nil
	}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("store: claim begin: %v", err)
		return nil
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx,
		`SELECT id::text, kind, payload::text, status, attempts,
		 COALESCE(lock_key,''), error, updated_at FROM tasks
		 WHERE status = 'queued' AND kind = ANY($1)
		 ORDER BY updated_at ASC LIMIT $2 FOR UPDATE SKIP LOCKED`, kinds, limit)
	if err != nil {
		log.Printf("store: claim select: %v", err)
		return nil
	}
	var ids []string
	out := []Operation{}
	for rows.Next() {
		var o Operation
		var payload, status string
		if err := rows.Scan(&o.ID, &o.Kind, &payload, &status,
			&o.Attempt, &o.IdempotencyKey, &o.Error, &o.UpdatedAt); err != nil {
			continue
		}
		var scope map[string]string
		if err := json.Unmarshal([]byte(payload), &scope); err == nil {
			o.ResourceKind, o.ResourceID = scope["resource_kind"], scope["resource_id"]
		}
		o.State = fromStored(status)
		out = append(out, o)
		ids = append(ids, o.ID)
	}
	rows.Close()
	if len(ids) == 0 {
		_ = tx.Commit(ctx)
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE tasks SET status = 'running', claimed_by = $2, claimed_at = now(),
		 updated_at = now() WHERE id::text = ANY($1)`, ids, worker); err != nil {
		log.Printf("store: claim update: %v", err)
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("store: claim commit: %v", err)
		return nil
	}
	for i := range out {
		out[i].State = "RUNNING"
	}
	return out
}
