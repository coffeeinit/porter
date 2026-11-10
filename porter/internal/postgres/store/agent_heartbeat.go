package store

import (
	"context"
	"fmt"
	"time"

	v1 "porter/controlplane/contract/v1"
)

// RecordAgentHeartbeat refreshes node liveness for the authenticated v2 agent.
// An offline node becomes ready; cordoned and draining states are never cleared
// by a heartbeat. It does not enroll a node or dispatch any command.
func (s *Store) RecordAgentHeartbeat(ctx context.Context, nodeID string, now time.Time) error {
	if err := validateOperationID("node", nodeID); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: heartbeat timestamp is required", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Fleet-node heartbeat: %w", err)
	}
	defer tx.Rollback()
	var previous v1.FleetNodeState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&previous); err != nil {
		return notFound("fleet node", nodeID, err)
	}
	next := previous
	if previous == v1.FleetNodeOffline {
		next = v1.FleetNodeReady
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.fleet_nodes
SET state = $1, last_seen_at = $2, updated_at = $2
WHERE id = $3`, next, now.UTC(), nodeID); err != nil {
		return fmt.Errorf("update Fleet-node heartbeat: %w", err)
	}
	if previous != next {
		if err := s.appendEvent(ctx, tx, "fleet_node.state_changed", eventRefs{fleetNodeID: nodeID}, map[string]any{
			"from": previous, "to": next, "reason": "authenticated_v2_heartbeat",
		}); err != nil {
			return err
		}
	}
	if err := pruneExpiredTelemetryTx(ctx, tx, now.UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Fleet-node heartbeat: %w", err)
	}
	return nil
}
