package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"porter/protocol"
)

var (
	ErrReplicaNotFound               = errors.New("replica not found")
	ErrReplicaNotScheduled           = errors.New("replica is not assigned to a VM and node")
	ErrReplicaOperationInProgress    = errors.New("replica already has an operation in progress")
	ErrReplicaLifecycleStateConflict = errors.New("replica state does not allow this lifecycle action")
	ErrReplicaApplySpecNotFound      = errors.New("replica apply specification not found")
	ErrInvalidReplicaLifecycleAction = errors.New("unsupported replica lifecycle action")
)

// QueueReplicaLifecycle validates the current replica and persists a VM lifecycle
// operation and command atomically. Start/restart reuse the original apply spec
// so the agent can reconstruct the VM with its existing image and network config.
func (s *Store) QueueReplicaLifecycle(ctx context.Context, replicaID, action string) (string, error) {
	kind := "vm." + action
	switch action {
	case "stop", "start", "restart":
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidReplicaLifecycleAction, action)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var nodeID, vmID, state string
	err = tx.QueryRowContext(ctx, `SELECT node_id,vm_id,state FROM replicas WHERE id=?`, replicaID).Scan(&nodeID, &vmID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrReplicaNotFound
	}
	if err != nil {
		return "", err
	}
	if nodeID == "" || vmID == "" {
		return "", ErrReplicaNotScheduled
	}

	var inProgress int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE resource_id=? AND state IN ('queued','running')`, replicaID).Scan(&inProgress); err != nil {
		return "", err
	}
	if inProgress != 0 {
		return "", ErrReplicaOperationInProgress
	}

	switch action {
	case "stop":
		if state != "running" {
			return "", fmt.Errorf("%w: cannot stop replica in %q state", ErrReplicaLifecycleStateConflict, state)
		}
	case "start":
		if state != "stopped" && state != "failed" {
			return "", fmt.Errorf("%w: cannot start replica in %q state", ErrReplicaLifecycleStateConflict, state)
		}
	case "restart":
		if state != "running" && state != "stopped" && state != "failed" {
			return "", fmt.Errorf("%w: cannot restart replica in %q state", ErrReplicaLifecycleStateConflict, state)
		}
	}

	var payload string
	err = tx.QueryRowContext(ctx, `
		SELECT c.payload
		FROM commands c
		JOIN operations o ON o.id=c.operation_id
		WHERE o.resource_id=? AND o.kind='vm.apply' AND c.kind='vm.apply'
		ORDER BY o.created_at DESC, c.created_at DESC
		LIMIT 1`, replicaID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrReplicaApplySpecNotFound
	}
	if err != nil {
		return "", err
	}
	var spec protocol.VMApplySpec
	if err := json.Unmarshal([]byte(payload), &spec); err != nil {
		return "", fmt.Errorf("decode persisted VM apply specification: %w", err)
	}
	if spec.VMID != vmID {
		return "", fmt.Errorf("persisted VM apply specification does not match replica VM")
	}
	if spec.ReplicaID == "" {
		spec.ReplicaID = replicaID
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode VM lifecycle command: %w", err)
	}

	now := time.Now().UTC()
	opID := NewID("op")
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,resource_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		opID, kind, replicaID, "queued", now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		NewID("cmd"), nodeID, opID, kind, string(raw), "queued", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,kind,resource_id,detail,created_at) VALUES(?,?,?,?,?)`,
		NewID("evt"), "operation.queued", opID, kind+" requested for replica "+replicaID, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return opID, nil
}
