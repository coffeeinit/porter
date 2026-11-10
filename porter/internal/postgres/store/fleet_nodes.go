package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

// CreateFleetNode persists an offline node record. It does not enroll an agent,
// accept a heartbeat, or dispatch a workload command.
func (s *Store) CreateFleetNode(ctx context.Context, name string) (v1.FleetNode, error) {
	name = strings.TrimSpace(name)
	if err := validateName("fleet node", name); err != nil {
		return v1.FleetNode{}, err
	}
	id, err := newID("node")
	if err != nil {
		return v1.FleetNode{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.FleetNode{}, fmt.Errorf("begin Fleet-node creation: %w", err)
	}
	defer tx.Rollback()
	node, err := scanFleetNode(tx.QueryRowContext(ctx, `
	INSERT INTO porter_control_plane.fleet_nodes (id, name, state)
	VALUES ($1, $2, $3)
	RETURNING id, name, state, enrollment_target, last_seen_at, created_at, updated_at`, id, name, v1.FleetNodeOffline))
	if err != nil {
		return v1.FleetNode{}, fmt.Errorf("create Fleet-node record: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "fleet_node.created", eventRefs{fleetNodeID: node.ID}, map[string]any{
		"name": node.Name, "state": node.State,
	}); err != nil {
		return v1.FleetNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.FleetNode{}, fmt.Errorf("commit Fleet-node creation: %w", err)
	}
	return node, nil
}

// SetFleetNodeState persists node metadata only; it never sends an agent or
// Firecracker lifecycle command. No heartbeat method is exposed by this store.
func (s *Store) SetFleetNodeState(ctx context.Context, id string, state v1.FleetNodeState) error {
	if !validFleetNodeState(state) {
		return fmt.Errorf("%w: unsupported Fleet-node state %q", ErrInvalidArgument, state)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Fleet-node state update: %w", err)
	}
	defer tx.Rollback()
	var previous v1.FleetNodeState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR UPDATE`, id).Scan(&previous); err != nil {
		return notFound("fleet node", id, err)
	}
	if previous == state {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.fleet_nodes SET state = $1, updated_at = now() WHERE id = $2`, state, id); err != nil {
		return fmt.Errorf("set Fleet-node state: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "fleet_node.state_changed", eventRefs{fleetNodeID: id}, map[string]any{
		"from": previous, "to": state,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Fleet-node state update: %w", err)
	}
	return nil
}

func (s *Store) ListFleetNodesPage(ctx context.Context, limit int, after *TimeCursor) ([]v1.FleetNode, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, name, state, enrollment_target, last_seen_at, created_at, updated_at
FROM porter_control_plane.fleet_nodes
WHERE $1::timestamptz IS NULL OR (created_at, id) > ($1::timestamptz, $2::text)
ORDER BY created_at, id
LIMIT $3`, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list Fleet-node page: %w", err)
	}
	return scanRows(rows, "Fleet node", scanFleetNode)
}

func scanFleetNode(row rowScanner) (v1.FleetNode, error) {
	var node v1.FleetNode
	var lastSeen sql.NullTime
	if err := row.Scan(&node.ID, &node.Name, &node.State, &node.EnrollmentTarget, &lastSeen, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return v1.FleetNode{}, err
	}
	if lastSeen.Valid {
		lastSeenAt := lastSeen.Time
		node.LastSeenAt = &lastSeenAt
	}
	return node, nil
}

func validFleetNodeState(state v1.FleetNodeState) bool {
	return state == v1.FleetNodeReady || state == v1.FleetNodeCordoned ||
		state == v1.FleetNodeDraining || state == v1.FleetNodeOffline
}
