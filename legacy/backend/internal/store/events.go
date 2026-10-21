package store

import (
	"context"
	"encoding/json"
	"log"
)

// This file persists the durable event spine + audit log + task ledger
// (migration 0018, task G5). The in-memory SSE hub (internal/event) stays the
// delivery fan-out; THESE tables are the truth.

// Event is one durable versioned fact.
type Event struct {
	Name           string
	Version        int
	Payload        map[string]interface{}
	ScopeType      string
	ScopeID        string
	ActorType      string
	ActorID        string
	CorrelationID  string
	IdempotencyKey string
	ResourceRef    string
}

// AppendEvent persists one event. Never deleted by app code.
func (s *Store) AppendEvent(e Event) error {
	payload, _ := json.Marshal(e.Payload)
	if e.Version == 0 {
		e.Version = 1
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO events (name, version, payload, scope_type, scope_id, actor_type, actor_id, correlation_id, idempotency_key, resource_ref)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		e.Name, e.Version, string(payload), e.ScopeType, e.ScopeID, e.ActorType, e.ActorID,
		e.CorrelationID, e.IdempotencyKey, e.ResourceRef)
	if err != nil {
		log.Printf("store: append event: %v", err)
	}
	return err
}

// AppendAudit persists one audit record. Secret values must never be passed in.
func (s *Store) AppendAudit(actorType, actorID, action, resourceRef, requestID, outcome string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO audit_events (actor_type, actor_id, action, resource_ref, request_id, outcome)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		actorType, actorID, action, resourceRef, requestID, outcome)
	if err != nil {
		log.Printf("store: append audit: %v", err)
	}
	return err
}

// Task states for durable operations.
const (
	TaskQueued             = "queued"
	TaskRunning            = "running"
	TaskWaiting            = "waiting"
	TaskSucceeded          = "succeeded"
	TaskFailed             = "failed"
	TaskRetrying           = "retrying"
	TaskCancelled          = "cancelled"
	TaskPartiallySucceeded = "partially_succeeded"
	TaskNeedsAttention     = "needs_attention"
)

// CreateTask inserts a durable task row and returns its id. A non-empty
// lockKey deduplicates (repeat call returns the existing row); empty lockKey
// always inserts (NULL never conflicts).
func (s *Store) CreateTask(kind, payload, lockKey string) (string, error) {
	var lock interface{}
	if lockKey != "" {
		lock = lockKey
	}
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO tasks (kind, payload, status, lock_key) VALUES ($1, $2::jsonb, 'queued', $3)
		 ON CONFLICT (lock_key) DO UPDATE SET kind = EXCLUDED.kind RETURNING id::text`,
		kind, payload, lock).Scan(&id)
	if err != nil {
		log.Printf("store: create task: %v", err)
	}
	return id, err
}

// UpdateTaskStatus transitions a task (idempotent: last write wins by updater).
func (s *Store) UpdateTaskStatus(id, status, progress, errText string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE tasks SET status=$2, progress=$3::jsonb, error=$4, attempts=attempts+1, updated_at=now() WHERE id::text=$1`,
		id, status, progress, errText)
	if err != nil {
		log.Printf("store: update task: %v", err)
	}
	return err
}
