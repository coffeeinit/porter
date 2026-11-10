package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

func (s *Store) CreateInstance(ctx context.Context, serviceID, releaseID string, ordinal int) (v1.Instance, error) {
	if ordinal <= 0 {
		return v1.Instance{}, fmt.Errorf("%w: instance ordinal must be positive", ErrInvalidArgument)
	}
	id, err := newID("ins")
	if err != nil {
		return v1.Instance{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Instance{}, fmt.Errorf("begin instance creation: %w", err)
	}
	defer tx.Rollback()
	var serviceState v1.ServiceState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, serviceID).Scan(&serviceState); err != nil {
		return v1.Instance{}, notFound("service", serviceID, err)
	}
	if serviceState != v1.ServiceActive {
		return v1.Instance{}, fmt.Errorf("%w: service %q is %q", ErrFailedPrecondition, serviceID, serviceState)
	}
	var releaseState v1.ReleaseState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.releases
WHERE id = $1 AND service_id = $2 FOR UPDATE`, releaseID, serviceID).Scan(&releaseState); err != nil {
		return v1.Instance{}, notFound("release", releaseID, err)
	}
	if releaseState != v1.ReleaseDeploying && releaseState != v1.ReleaseActive {
		return v1.Instance{}, fmt.Errorf("%w: cannot create an instance for release %q in state %q", ErrFailedPrecondition, releaseID, releaseState)
	}

	instance, err := scanInstance(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.instances
    (id, service_id, release_id, ordinal, runtime, state, health)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, service_id, release_id, ordinal, runtime, state, health, node_id, created_at, updated_at`,
		id, serviceID, releaseID, ordinal, v1.FirecrackerRuntime,
		v1.InstancePending, v1.HealthUnknown))
	if err != nil {
		return v1.Instance{}, fmt.Errorf("create instance record: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "instance.created", eventRefs{
		serviceID: instance.ServiceID, releaseID: instance.ReleaseID, instanceID: instance.ID,
	}, map[string]any{"ordinal": instance.Ordinal, "state": instance.State, "runtime": instance.Runtime}); err != nil {
		return v1.Instance{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Instance{}, fmt.Errorf("commit instance record: %w", err)
	}
	return instance, nil
}

func (s *Store) GetInstance(ctx context.Context, id string) (v1.Instance, error) {
	instance, err := scanInstance(s.db.QueryRowContext(ctx, instanceSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.Instance{}, notFound("instance", id, err)
	}
	return instance, nil
}

func (s *Store) ListInstances(ctx context.Context, releaseID string) ([]v1.Instance, error) {
	rows, err := s.db.QueryContext(ctx, instanceSelect+` WHERE release_id = $1 ORDER BY created_at, id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()
	instances := make([]v1.Instance, 0)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan instance: %w", err)
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate instances: %w", err)
	}
	return instances, nil
}

const instanceSelect = `
SELECT id, service_id, release_id, ordinal, runtime, state, health, node_id, created_at, updated_at
FROM porter_control_plane.instances`

func scanInstance(row rowScanner) (v1.Instance, error) {
	var instance v1.Instance
	var nodeID sql.NullString
	err := row.Scan(&instance.ID, &instance.ServiceID, &instance.ReleaseID, &instance.Ordinal,
		&instance.Runtime, &instance.State, &instance.Health, &nodeID,
		&instance.CreatedAt, &instance.UpdatedAt)
	if err != nil {
		return v1.Instance{}, err
	}
	if nodeID.Valid {
		instance.NodeID = nodeID.String
	}
	return instance, nil
}

func (s *Store) SetInstanceState(ctx context.Context, id string, state v1.InstanceState, health v1.InstanceHealth) error {
	if !validInstanceState(state) {
		return fmt.Errorf("%w: unsupported instance state %q", ErrInvalidArgument, state)
	}
	if !validInstanceHealth(health) {
		return fmt.Errorf("%w: unsupported instance health %q", ErrInvalidArgument, health)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin instance state update: %w", err)
	}
	defer tx.Rollback()
	var previousState v1.InstanceState
	var previousHealth v1.InstanceHealth
	var serviceID, releaseID string
	if err := tx.QueryRowContext(ctx, `
SELECT service_id, release_id, state, health FROM porter_control_plane.instances WHERE id = $1 FOR UPDATE`, id).
		Scan(&serviceID, &releaseID, &previousState, &previousHealth); err != nil {
		return notFound("instance", id, err)
	}
	if previousState == state && previousHealth == health {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.instances SET state = $1, health = $2, updated_at = now() WHERE id = $3`, state, health, id); err != nil {
		return fmt.Errorf("set instance state: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "instance.state_changed", eventRefs{
		serviceID: serviceID, releaseID: releaseID, instanceID: id,
	}, map[string]any{"from_state": previousState, "to_state": state, "from_health": previousHealth, "to_health": health}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit instance state update: %w", err)
	}
	return nil
}

// SetInstanceNode persists optional fleet placement only; it does not dispatch
// a command or start a Firecracker guest.
func (s *Store) SetInstanceNode(ctx context.Context, id, nodeID string) error {
	nodeID = strings.TrimSpace(nodeID)
	var placement any
	if nodeID != "" {
		placement = nodeID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin instance placement update: %w", err)
	}
	defer tx.Rollback()
	var previousNodeID sql.NullString
	var serviceID, releaseID string
	if err := tx.QueryRowContext(ctx, `
SELECT service_id, release_id, node_id FROM porter_control_plane.instances WHERE id = $1 FOR UPDATE`, id).
		Scan(&serviceID, &releaseID, &previousNodeID); err != nil {
		return notFound("instance", id, err)
	}
	if (previousNodeID.Valid && previousNodeID.String == nodeID) || (!previousNodeID.Valid && nodeID == "") {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.instances SET node_id = $1, updated_at = now() WHERE id = $2`, placement, id); err != nil {
		return fmt.Errorf("set instance fleet node: %w", err)
	}
	affectedNodeID := nodeID
	if affectedNodeID == "" && previousNodeID.Valid {
		affectedNodeID = previousNodeID.String
	}
	previous := ""
	if previousNodeID.Valid {
		previous = previousNodeID.String
	}
	if err := s.appendEvent(ctx, tx, "instance.node_changed", eventRefs{
		serviceID: serviceID, releaseID: releaseID, instanceID: id, fleetNodeID: affectedNodeID,
	}, map[string]any{"from_node_id": previous, "to_node_id": nodeID}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit instance placement update: %w", err)
	}
	return nil
}

func validInstanceState(state v1.InstanceState) bool {
	switch state {
	case v1.InstancePending, v1.InstanceProvisioning, v1.InstanceRunning, v1.InstanceStopped,
		v1.InstanceFailed, v1.InstanceDeleting, v1.InstanceDeleted:
		return true
	default:
		return false
	}
}

func validInstanceHealth(health v1.InstanceHealth) bool {
	return health == v1.HealthUnknown || health == v1.HealthHealthy || health == v1.HealthUnhealthy
}
