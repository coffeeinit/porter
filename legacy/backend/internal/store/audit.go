// This file is the structured read path for audit_events (migration 0018).
// AppendAudit in events.go persists rows without IP; AppendAuditRecord adds
// the client address. Secret values must never be passed into either
// (SRS §26: no secret values in audit records).
package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

// AuditEvent is one row of audit_events.
type AuditEvent struct {
	ID          int64     `json:"id"`
	ActorType   string    `json:"actor_type"`
	ActorID     string    `json:"actor_id"`
	Action      string    `json:"action"`
	ResourceRef string    `json:"resource_ref"`
	RequestID   string    `json:"request_id"`
	IP          string    `json:"ip,omitempty"`
	Outcome     string    `json:"outcome"` // allowed | denied | failed
	At          time.Time `json:"at"`
}

// AuditFilter bounds a ListAuditEvents query. Zero Limit means the default;
// the store clamps to 1..500.
type AuditFilter struct {
	Actor    string // exact actor_id match
	Action   string // exact action match
	Resource string // exact resource_ref match
	Limit    int
	Offset   int
}

const (
	auditDefaultLimit = 100
	auditMaxLimit     = 500
)

// clampAuditWindow applies the limit/offset bounds (default 100, max 500).
func clampAuditWindow(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = auditDefaultLimit
	}
	if limit > auditMaxLimit {
		limit = auditMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// AppendAuditRecord persists one audit record including the client IP (stored
// NULL when empty). Never fails a request path: callers may ignore the error;
// it is already logged.
func (s *Store) AppendAuditRecord(actorType, actorID, action, resourceRef, requestID, ip, outcome string) error {
	var inet any
	if ip != "" {
		inet = ip
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO audit_events (actor_type, actor_id, action, resource_ref, request_id, ip, outcome)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		actorType, actorID, action, resourceRef, requestID, inet, outcome)
	if err != nil {
		log.Printf("store: append audit record: %v", err)
	}
	return err
}

// ListAuditEvents returns one page of audit rows (newest first) plus the
// unfiltered total for the given filter. Empty rows slice (not nil) when no
// events match.
func (s *Store) ListAuditEvents(f AuditFilter) ([]AuditEvent, int, error) {
	limit, offset := clampAuditWindow(f.Limit, f.Offset)
	where := "TRUE"
	args := []any{}
	if f.Actor != "" {
		args = append(args, f.Actor)
		where += fmt.Sprintf(" AND actor_id = $%d", len(args))
	}
	if f.Action != "" {
		args = append(args, f.Action)
		where += fmt.Sprintf(" AND action = $%d", len(args))
	}
	if f.Resource != "" {
		args = append(args, f.Resource)
		where += fmt.Sprintf(" AND resource_ref = $%d", len(args))
	}

	var total int
	if err := s.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_events WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, actor_type, actor_id, action, resource_ref, request_id,
		        COALESCE(host(ip), ''), outcome, at
		 FROM audit_events WHERE `+where+
			fmt.Sprintf(" ORDER BY at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.Action, &e.ResourceRef,
			&e.RequestID, &e.IP, &e.Outcome, &e.At); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
