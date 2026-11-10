package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

var (
	ErrIdempotencyConflict      = errors.New("idempotency key conflicts with a different operation")
	ErrOperationInProgress      = errors.New("instance already has an active operation")
	ErrCommandNodeMismatch      = errors.New("command belongs to another node")
	ErrCommandOperationMismatch = errors.New("operation id does not match command")
	ErrCommandKindMismatch      = errors.New("command kind does not match active command")
	ErrCommandAttemptMismatch   = errors.New("command attempt does not match active attempt")
	ErrCommandLeaseMismatch     = errors.New("command lease token does not match active lease")
	ErrCommandLeaseExpired      = errors.New("command lease has expired")
	ErrCommandStateConflict     = errors.New("command is not in a completable state")
	ErrCommandResultConflict    = errors.New("command result conflicts with the recorded result")
)

const (
	minCommandLeaseDuration = time.Second
	maxCommandLeaseDuration = 15 * time.Minute
	maxCommandResultError   = 4096
)

// VMDeleteCommandPayload is the typed payload for the legacy protocol's VM delete kind.
type VMDeleteCommandPayload struct {
	VMID string `json:"vm_id"`
}

// AgentCommandSpec is a closed, kind-discriminated payload union. Unknown kinds
// such as shell.exec and raw command strings are never accepted.
type AgentCommandSpec struct {
	Kind            v1.AgentCommandKind
	VMApply         *protocol.VMApplySpec
	VMDelete        *VMDeleteCommandPayload
	SnapshotCreate  *protocol.SnapshotSpec
	SnapshotRestore *protocol.SnapshotRestoreCommand
	VolumeResize    *protocol.VolumeResizeSpec
}

type QueueOperationRequest struct {
	InstanceID     string
	NodeID         string
	IdempotencyKey string
	Command        AgentCommandSpec
}

// QueueOperation persists a typed operation and its command in one transaction.
// It does not contact or dispatch to a Fleet agent.
func (s *Store) QueueOperation(ctx context.Context, request QueueOperationRequest) (v1.Operation, error) {
	if err := validateOperationID("instance", request.InstanceID); err != nil {
		return v1.Operation{}, err
	}
	if err := validateOperationID("node", request.NodeID); err != nil {
		return v1.Operation{}, err
	}
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 || strings.TrimSpace(request.IdempotencyKey) != request.IdempotencyKey {
		return v1.Operation{}, fmt.Errorf("%w: idempotency key must contain 1 to 128 non-space characters", ErrInvalidArgument)
	}
	payload, err := request.Command.marshalPayload()
	if err != nil {
		return v1.Operation{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Operation{}, fmt.Errorf("begin operation queue: %w", err)
	}
	defer tx.Rollback()

	var projectID, serviceID, releaseID, instanceState string
	var runtimeVMID sql.NullString
	var assignedNodeID, nodeState sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT s.project_id, i.service_id, i.release_id, i.state, i.node_id, n.state, i.runtime_vm_id
FROM porter_control_plane.instances i
JOIN porter_control_plane.services s ON s.id = i.service_id
LEFT JOIN porter_control_plane.fleet_nodes n ON n.id = i.node_id
WHERE i.id = $1
FOR UPDATE OF i`, request.InstanceID).Scan(
		&projectID, &serviceID, &releaseID, &instanceState, &assignedNodeID, &nodeState, &runtimeVMID,
	); err != nil {
		return v1.Operation{}, notFound("instance", request.InstanceID, err)
	}

	fingerprint, err := operationFingerprint(projectID, serviceID, request.InstanceID, request.NodeID, request.Command.Kind, payload)
	if err != nil {
		return v1.Operation{}, err
	}
	var existingID, existingFingerprint string
	err = tx.QueryRowContext(ctx, `
SELECT id, request_sha256
FROM porter_control_plane.operations
WHERE project_id = $1 AND idempotency_key = $2`, projectID, request.IdempotencyKey).
		Scan(&existingID, &existingFingerprint)
	if err == nil {
		if subtle.ConstantTimeCompare([]byte(existingFingerprint), []byte(fingerprint)) != 1 {
			return v1.Operation{}, ErrIdempotencyConflict
		}
		return scanOperation(tx.QueryRowContext(ctx, operationSelect+` WHERE o.id = $1`, existingID))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v1.Operation{}, fmt.Errorf("check operation idempotency key: %w", err)
	}
	if request.Command.VMApply != nil &&
		(request.Command.VMApply.ProjectID != projectID || request.Command.VMApply.ReplicaID != request.InstanceID) {
		return v1.Operation{}, fmt.Errorf("%w: VM command payload Project/Instance scope does not match the operation", ErrInvalidArgument)
	}
	if request.Command.SnapshotCreate != nil {
		if err := request.Command.SnapshotCreate.Validate(); err != nil {
			return v1.Operation{}, fmt.Errorf("%w: invalid snapshot create payload", ErrInvalidArgument)
		}
		if !runtimeVMID.Valid || request.Command.SnapshotCreate.VMID != runtimeVMID.String {
			return v1.Operation{}, fmt.Errorf("%w: snapshot VM identity is not the persisted instance runtime", ErrFailedPrecondition)
		}
	}
	if request.Command.SnapshotRestore != nil {
		if err := request.Command.SnapshotRestore.Validate(); err != nil {
			return v1.Operation{}, fmt.Errorf("%w: invalid snapshot restore payload", ErrInvalidArgument)
		}
		if !runtimeVMID.Valid || request.Command.SnapshotRestore.VMID != runtimeVMID.String {
			return v1.Operation{}, fmt.Errorf("%w: snapshot VM identity is not the persisted instance runtime", ErrFailedPrecondition)
		}
	}

	if !assignedNodeID.Valid || assignedNodeID.String != request.NodeID || !nodeState.Valid {
		return v1.Operation{}, fmt.Errorf("%w: instance is not assigned to the requested Fleet node", ErrFailedPrecondition)
	}
	if nodeState.String != string(v1.FleetNodeReady) {
		return v1.Operation{}, fmt.Errorf("%w: Fleet node is not ready", ErrFailedPrecondition)
	}
	if !commandAllowedForInstanceState(request.Command.Kind, instanceState) {
		return v1.Operation{}, fmt.Errorf("%w: command is not allowed for instance state %q", ErrFailedPrecondition, instanceState)
	}

	var inProgress bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM porter_control_plane.operations
    WHERE instance_id = $1 AND state IN ('queued', 'running')
)`, request.InstanceID).Scan(&inProgress); err != nil {
		return v1.Operation{}, fmt.Errorf("check active instance operation: %w", err)
	}
	if inProgress {
		return v1.Operation{}, ErrOperationInProgress
	}

	now := time.Now().UTC()
	operationID, err := newID("op")
	if err != nil {
		return v1.Operation{}, err
	}
	commandID, err := newID("cmd")
	if err != nil {
		return v1.Operation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.operations
    (id, project_id, service_id, instance_id, kind, state, idempotency_key, request_sha256, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'queued', $6, $7, $8, $8)`,
		operationID, projectID, serviceID, request.InstanceID, request.Command.Kind,
		request.IdempotencyKey, fingerprint, now); err != nil {
		return v1.Operation{}, fmt.Errorf("insert operation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.agent_commands
    (id, operation_id, project_id, service_id, instance_id, node_id, kind, payload_version, payload, state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8::jsonb, 'queued', $9, $9)`,
		commandID, operationID, projectID, serviceID, request.InstanceID, request.NodeID,
		request.Command.Kind, string(payload), now); err != nil {
		return v1.Operation{}, fmt.Errorf("insert typed agent command: %w", err)
	}
	var snapshotID string
	switch request.Command.Kind {
	case v1.AgentCommandSnapshotCreate:
		snapshotID = request.Command.SnapshotCreate.SnapshotID
		if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.snapshot_metadata
	(id, project_id, service_id, instance_id, consistency, capture_status, node_affinity_id,
	 capture_operation_id, runtime_vm_id)
VALUES ($1, $2, $3, $4, 'crash_consistent', 'queued', $5, $6, $7)`,
			snapshotID, projectID, serviceID, request.InstanceID, request.NodeID, operationID, runtimeVMID.String); err != nil {
			return v1.Operation{}, fmt.Errorf("insert queued snapshot metadata: %w", err)
		}
	case v1.AgentCommandSnapshotRestore:
		snapshotID = request.Command.SnapshotRestore.SnapshotID
		result, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.snapshot_metadata
SET restore_status = 'queued', restore_operation_id = $1
WHERE id = $2 AND project_id = $3 AND service_id = $4 AND instance_id = $5
	AND node_affinity_id = $6 AND runtime_vm_id = $7 AND capture_status = 'captured'
	AND artifact_sha256 = $8 AND size_bytes = $9 AND consistency = 'crash_consistent'`,
			operationID, snapshotID, projectID, serviceID, request.InstanceID, request.NodeID,
			runtimeVMID.String, request.Command.SnapshotRestore.ArtifactSHA256, request.Command.SnapshotRestore.SizeBytes)
		if err != nil {
			return v1.Operation{}, fmt.Errorf("link queued snapshot restore metadata: %w", err)
		}
		if err := requireOneRow(result, "captured snapshot", snapshotID); err != nil {
			return v1.Operation{}, fmt.Errorf("%w: snapshot is not captured on this instance/node or its integrity metadata changed", ErrFailedPrecondition)
		}
	}
	if err := s.appendEvent(ctx, tx, "operation.queued", eventRefs{
		projectID: projectID, serviceID: serviceID, instanceID: request.InstanceID,
		fleetNodeID: request.NodeID, operationID: operationID, commandID: commandID, snapshotID: snapshotID,
	}, map[string]any{"kind": request.Command.Kind, "state": v1.OperationQueued, "snapshot_id": snapshotID}); err != nil {
		return v1.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Operation{}, fmt.Errorf("commit operation queue: %w", err)
	}
	return v1.Operation{
		ID: operationID, ProjectID: projectID, ServiceID: serviceID, InstanceID: request.InstanceID,
		ResourceID: request.InstanceID, Kind: request.Command.Kind, State: v1.OperationQueued,
		CommandID: commandID, CommandState: v1.AgentCommandQueued, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (spec AgentCommandSpec) marshalPayload() (json.RawMessage, error) {
	var payload any
	variants := 0
	switch spec.Kind {
	case v1.AgentCommandVMApply, v1.AgentCommandVMStart, v1.AgentCommandVMStop, v1.AgentCommandVMRestart:
		if spec.VMApply != nil {
			variants++
			payload = spec.VMApply
		}
		if !validVMApplySpec(spec.VMApply) {
			return nil, fmt.Errorf("%w: VM command requires a complete typed VM apply specification", ErrInvalidArgument)
		}
	case v1.AgentCommandVMDelete:
		if spec.VMDelete != nil {
			variants++
			payload = spec.VMDelete
		}
		if spec.VMDelete == nil || !validOperationID(spec.VMDelete.VMID) {
			return nil, fmt.Errorf("%w: VM delete requires a valid vm_id", ErrInvalidArgument)
		}
	case v1.AgentCommandSnapshotCreate:
		if spec.SnapshotCreate != nil {
			variants++
			payload = spec.SnapshotCreate
		}
		if spec.SnapshotCreate == nil || spec.SnapshotCreate.Validate() != nil {
			return nil, fmt.Errorf("%w: snapshot.create requires valid IDs and crash_consistent semantics", ErrInvalidArgument)
		}
	case v1.AgentCommandSnapshotRestore:
		if spec.SnapshotRestore != nil {
			variants++
			payload = spec.SnapshotRestore
		}
		if spec.SnapshotRestore == nil || spec.SnapshotRestore.Validate() != nil {
			return nil, fmt.Errorf("%w: snapshot.restore requires identity and expected integrity metadata", ErrInvalidArgument)
		}
	case v1.AgentCommandVolumeResize:
		if spec.VolumeResize != nil {
			variants++
			payload = spec.VolumeResize
		}
		if spec.VolumeResize == nil || !validOperationID(spec.VolumeResize.VolumeID) || spec.VolumeResize.SizeMB < 1 {
			return nil, fmt.Errorf("%w: volume.resize requires a valid volume_id and positive size_mb", ErrInvalidArgument)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported typed agent command kind", ErrInvalidArgument)
	}
	if variants != 1 || commandPayloadVariantCount(spec) != 1 {
		return nil, fmt.Errorf("%w: exactly one payload matching the command kind is required", ErrInvalidArgument)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode typed agent command payload: %w", err)
	}
	return encoded, nil
}

func commandPayloadVariantCount(spec AgentCommandSpec) int {
	count := 0
	for _, present := range []bool{
		spec.VMApply != nil, spec.VMDelete != nil, spec.SnapshotCreate != nil,
		spec.SnapshotRestore != nil, spec.VolumeResize != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func validVMApplySpec(spec *protocol.VMApplySpec) bool {
	return spec != nil && validOperationID(spec.VMID) && validOperationID(spec.ReplicaID) &&
		validOperationID(spec.ProjectID) && validOperationID(spec.DeploymentID) &&
		strings.TrimSpace(spec.Image) != "" && strings.TrimSpace(spec.KernelPath) != "" &&
		strings.TrimSpace(spec.RootfsPath) != "" && spec.VCPUs > 0 && spec.MemoryMB > 0 && spec.DiskMB > 0
}

func validateOperationID(kind, id string) error {
	if !validOperationID(id) {
		return fmt.Errorf("%w: %s ID must contain 1 to 128 non-space characters without slashes", ErrInvalidArgument, kind)
	}
	return nil
}

func validOperationID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n\t ")
}

func commandAllowedForInstanceState(kind v1.AgentCommandKind, state string) bool {
	switch kind {
	case v1.AgentCommandVMApply:
		return state == string(v1.InstancePending) || state == string(v1.InstanceProvisioning)
	case v1.AgentCommandVMDelete:
		return state == string(v1.InstanceDeleting)
	case v1.AgentCommandVMStart:
		return state == string(v1.InstanceStopped) || state == string(v1.InstanceFailed)
	case v1.AgentCommandVMStop:
		return state == string(v1.InstanceRunning)
	case v1.AgentCommandVMRestart:
		return state == string(v1.InstanceRunning) || state == string(v1.InstanceStopped) || state == string(v1.InstanceFailed)
	case v1.AgentCommandSnapshotCreate:
		return state == string(v1.InstanceRunning)
	case v1.AgentCommandSnapshotRestore:
		return state == string(v1.InstanceStopped) || state == string(v1.InstanceFailed)
	case v1.AgentCommandVolumeResize:
		return state == string(v1.InstanceRunning) || state == string(v1.InstanceStopped)
	default:
		return false
	}
}

func operationFingerprint(projectID, serviceID, instanceID, nodeID string, kind v1.AgentCommandKind, payload []byte) (string, error) {
	request := struct {
		ProjectID  string              `json:"project_id"`
		ServiceID  string              `json:"service_id"`
		InstanceID string              `json:"instance_id"`
		NodeID     string              `json:"node_id"`
		Kind       v1.AgentCommandKind `json:"kind"`
		Payload    json.RawMessage     `json:"payload"`
	}{projectID, serviceID, instanceID, nodeID, kind, payload}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("fingerprint operation request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

const operationSelect = `
SELECT o.id, o.project_id, o.service_id, o.instance_id, o.kind, o.state, o.error,
       c.id, c.state, c.attempt, c.lease_expires_at, o.created_at, o.updated_at
FROM porter_control_plane.operations o
JOIN porter_control_plane.agent_commands c ON c.operation_id = o.id`

func (s *Store) GetOperation(ctx context.Context, id string) (v1.Operation, error) {
	if err := validateOperationID("operation", id); err != nil {
		return v1.Operation{}, err
	}
	operation, err := scanOperation(s.db.QueryRowContext(ctx, operationSelect+` WHERE o.id = $1`, id))
	if err != nil {
		return v1.Operation{}, notFound("operation", id, err)
	}
	return operation, nil
}

func (s *Store) ListOperationsPage(ctx context.Context, limit int, after *TimeCursor) ([]v1.Operation, error) {
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
	rows, err := s.db.QueryContext(ctx, operationSelect+`
WHERE $1::timestamptz IS NULL OR (o.created_at, o.id) < ($1::timestamptz, $2::text)
ORDER BY o.created_at DESC, o.id DESC
LIMIT $3`, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list operation page: %w", err)
	}
	return scanRows(rows, "operation", scanOperation)
}

func scanOperation(row rowScanner) (v1.Operation, error) {
	var operation v1.Operation
	var kind string
	var state, commandState string
	var leaseExpiry sql.NullTime
	if err := row.Scan(&operation.ID, &operation.ProjectID, &operation.ServiceID, &operation.InstanceID,
		&kind, &state, &operation.Error, &operation.CommandID, &commandState, &operation.Attempt,
		&leaseExpiry, &operation.CreatedAt, &operation.UpdatedAt); err != nil {
		return v1.Operation{}, err
	}
	operation.ResourceID = operation.InstanceID
	operation.Kind = v1.AgentCommandKind(kind)
	operation.State = v1.OperationState(state)
	operation.CommandState = v1.AgentCommandState(commandState)
	if leaseExpiry.Valid {
		value := leaseExpiry.Time
		operation.LeaseExpiresAt = &value
	}
	return operation, nil
}

// LeaseCommands claims queued commands for a ready node, incrementing attempt
// once and returning a fresh, short-lived token. It is a store primitive only;
// no HTTP route or agent authentication is wired to it.
func (s *Store) LeaseCommands(ctx context.Context, nodeID string, limit int, duration time.Duration) ([]v1.AgentCommandLease, error) {
	return s.LeaseCommandsAt(ctx, nodeID, limit, duration, time.Now().UTC())
}

// LeaseCommandsAt exposes the clock for deterministic tests and controlled
// schedulers. It retains the same validation and transactional fencing.
func (s *Store) LeaseCommandsAt(ctx context.Context, nodeID string, limit int, duration time.Duration, now time.Time) ([]v1.AgentCommandLease, error) {
	return s.leaseCommandsAt(ctx, nodeID, limit, duration, now, nil)
}

// LeaseVMCommandsAt leases only the VM lifecycle kinds supported by the
// opt-in v2 agent dispatcher. Other typed kinds remain queued for a later slice.
func (s *Store) LeaseVMCommandsAt(ctx context.Context, nodeID string, limit int, duration time.Duration, now time.Time) ([]v1.AgentCommandLease, error) {
	return s.leaseCommandsAt(ctx, nodeID, limit, duration, now, []string{"vm.apply", "vm.delete", "vm.start", "vm.stop", "vm.restart"})
}

func (s *Store) leaseCommandsAt(ctx context.Context, nodeID string, limit int, duration time.Duration, now time.Time, allowedKinds []string) ([]v1.AgentCommandLease, error) {
	if err := validateOperationID("node", nodeID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 || duration < minCommandLeaseDuration || duration > maxCommandLeaseDuration || now.IsZero() {
		return nil, fmt.Errorf("%w: invalid command lease limit, duration, or timestamp", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin command lease: %w", err)
	}
	defer tx.Rollback()
	var nodeState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR SHARE`, nodeID).Scan(&nodeState); err != nil {
		return nil, notFound("Fleet node", nodeID, err)
	}
	if nodeState != string(v1.FleetNodeReady) {
		return nil, fmt.Errorf("%w: Fleet node is not ready", ErrFailedPrecondition)
	}
	query := `
SELECT id, operation_id, project_id, service_id, instance_id, kind, payload, created_at, attempt
FROM porter_control_plane.agent_commands
WHERE node_id = $1 AND state = 'queued'`
	args := []any{nodeID}
	if len(allowedKinds) > 0 {
		placeholders := make([]string, 0, len(allowedKinds))
		for _, kind := range allowedKinds {
			args = append(args, kind)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		query += " AND kind IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d FOR UPDATE SKIP LOCKED", len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("select queued agent commands: %w", err)
	}
	type queuedCommand struct {
		id, operationID, projectID, serviceID, instanceID, kind string
		payload                                                 []byte
		created                                                 time.Time
		attempt                                                 int
	}
	queued := make([]queuedCommand, 0, limit)
	for rows.Next() {
		var command queuedCommand
		if err := rows.Scan(&command.id, &command.operationID, &command.projectID, &command.serviceID,
			&command.instanceID, &command.kind, &command.payload, &command.created, &command.attempt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan queued agent command: %w", err)
		}
		queued = append(queued, command)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read queued agent commands: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close queued agent command rows: %w", err)
	}

	leases := make([]v1.AgentCommandLease, 0, len(queued))
	for _, command := range queued {
		token, tokenHash, err := newLeaseToken()
		if err != nil {
			return nil, err
		}
		expiresAt := now.UTC().Add(duration)
		newAttempt := command.attempt + 1
		commandUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.agent_commands
SET state = 'leased', attempt = $2, lease_token_hash = $3, lease_expires_at = $4, updated_at = $5
		WHERE id = $1 AND state = 'queued'`, command.id, newAttempt, tokenHash, expiresAt, now.UTC())
		if err != nil {
			return nil, fmt.Errorf("write command lease: %w", err)
		}
		if err := requireOneRow(commandUpdate, "agent command", command.id); err != nil {
			return nil, err
		}
		operationUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.operations SET state = 'running', updated_at = $2
		WHERE id = $1 AND state = 'queued'`, command.operationID, now.UTC())
		if err != nil {
			return nil, fmt.Errorf("mark operation running: %w", err)
		}
		if err := requireOneRow(operationUpdate, "operation", command.operationID); err != nil {
			return nil, err
		}
		snapshotID, err := transitionSnapshotForOperation(ctx, tx, v1.AgentCommandKind(command.kind), command.operationID, true)
		if err != nil {
			return nil, err
		}
		if err := s.appendEvent(ctx, tx, "command.leased", eventRefs{
			projectID: command.projectID, serviceID: command.serviceID, instanceID: command.instanceID,
			fleetNodeID: nodeID, operationID: command.operationID, commandID: command.id, snapshotID: snapshotID,
		}, map[string]any{"kind": command.kind, "attempt": newAttempt, "lease_expires_at": expiresAt, "snapshot_id": snapshotID}); err != nil {
			return nil, err
		}
		leases = append(leases, v1.AgentCommandLease{
			ID: command.id, NodeID: nodeID, OperationID: command.operationID, Attempt: newAttempt,
			LeaseToken: token, LeaseExpiresAt: expiresAt, Kind: v1.AgentCommandKind(command.kind),
			Payload: append(json.RawMessage(nil), command.payload...), CreatedAt: command.created,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit command lease: %w", err)
	}
	return leases, nil
}

// LeaseProtocolCommandsAt leases only the typed VM and snapshot commands
// supported by the v2 agent dispatcher, then converts them to the shared wire.
func (s *Store) LeaseProtocolCommandsAt(ctx context.Context, nodeID string, limit int, duration time.Duration, now time.Time) ([]protocol.CommandV2, error) {
	leases, err := s.leaseCommandsAt(ctx, nodeID, limit, duration, now, []string{
		"vm.apply", "vm.delete", "vm.start", "vm.stop", "vm.restart", "snapshot.create", "snapshot.restore",
	})
	if err != nil {
		return nil, err
	}
	commands := make([]protocol.CommandV2, 0, len(leases))
	for _, lease := range leases {
		command := protocol.CommandV2{
			ProtocolVersion: protocol.VersionV2,
			ID:              lease.ID, NodeID: lease.NodeID, OperationID: lease.OperationID, Attempt: lease.Attempt,
			Kind: string(lease.Kind), LeaseToken: lease.LeaseToken, LeaseExpiresAt: lease.LeaseExpiresAt,
			Payload: append([]byte(nil), lease.Payload...), State: string(v1.AgentCommandLeased), CreatedAt: lease.CreatedAt,
		}
		if err := command.ValidateAt(now); err != nil {
			return nil, fmt.Errorf("validate persisted v2 command lease: %w", err)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func newLeaseToken() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("generate command lease token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

// RequeueExpiredCommands returns expired leases to queued state without
// incrementing attempt; the next successful lease increments it exactly once.
func (s *Store) RequeueExpiredCommands(ctx context.Context, now time.Time, limit int) (int, error) {
	return s.requeueExpiredCommands(ctx, "", now, limit)
}

// RequeueExpiredCommandsForNode limits retry maintenance to one authenticated
// Fleet node so an agent cannot trigger state changes for another node.
func (s *Store) RequeueExpiredCommandsForNode(ctx context.Context, nodeID string, now time.Time, limit int) (int, error) {
	if err := validateOperationID("node", nodeID); err != nil {
		return 0, err
	}
	return s.requeueExpiredCommands(ctx, nodeID, now, limit)
}

func (s *Store) requeueExpiredCommands(ctx context.Context, nodeID string, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("%w: invalid lease-expiry time or batch limit", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin expired lease retry: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT id, operation_id, project_id, service_id, instance_id, node_id, kind, attempt
FROM porter_control_plane.agent_commands
WHERE state = 'leased' AND lease_expires_at <= $1
  AND ($3::text IS NULL OR node_id = $3)
ORDER BY lease_expires_at, id
LIMIT $2
FOR UPDATE SKIP LOCKED`, now.UTC(), limit, nullableID(nodeID))
	if err != nil {
		return 0, fmt.Errorf("select expired command leases: %w", err)
	}
	type expiredCommand struct {
		id, operationID, projectID, serviceID, instanceID, nodeID, kind string
		attempt                                                         int
	}
	expired := make([]expiredCommand, 0, limit)
	for rows.Next() {
		var command expiredCommand
		if err := rows.Scan(&command.id, &command.operationID, &command.projectID, &command.serviceID,
			&command.instanceID, &command.nodeID, &command.kind, &command.attempt); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan expired command lease: %w", err)
		}
		expired = append(expired, command)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("read expired command leases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close expired command rows: %w", err)
	}
	for _, command := range expired {
		commandUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.agent_commands
SET state = 'queued', lease_token_hash = NULL, lease_expires_at = NULL, updated_at = $2
		WHERE id = $1 AND state = 'leased' AND lease_expires_at <= $2`, command.id, now.UTC())
		if err != nil {
			return 0, fmt.Errorf("requeue expired command: %w", err)
		}
		if err := requireOneRow(commandUpdate, "agent command", command.id); err != nil {
			return 0, err
		}
		operationUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.operations SET state = 'queued', updated_at = $2
		WHERE id = $1 AND state = 'running'`, command.operationID, now.UTC())
		if err != nil {
			return 0, fmt.Errorf("requeue expired operation: %w", err)
		}
		if err := requireOneRow(operationUpdate, "operation", command.operationID); err != nil {
			return 0, err
		}
		snapshotID, err := transitionSnapshotForOperation(ctx, tx, v1.AgentCommandKind(command.kind), command.operationID, false)
		if err != nil {
			return 0, err
		}
		if err := s.appendEvent(ctx, tx, "operation.retry_queued", eventRefs{
			projectID: command.projectID, serviceID: command.serviceID, instanceID: command.instanceID,
			fleetNodeID: command.nodeID, operationID: command.operationID, commandID: command.id, snapshotID: snapshotID,
		}, map[string]any{"kind": command.kind, "attempt": command.attempt, "reason": "lease_expired", "snapshot_id": snapshotID}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit expired command retry: %w", err)
	}
	return len(expired), nil
}

// RecordCommandResult commits the typed result, operation transition, resource
// lifecycle update, and event together. Exact duplicate receipts are idempotent.
func (s *Store) RecordCommandResult(ctx context.Context, result v1.AgentCommandResult) error {
	return s.recordCommandResult(ctx, result, nil)
}

func (s *Store) recordCommandResult(ctx context.Context, result v1.AgentCommandResult, receipt *protocol.SnapshotArtifactReceipt) error {
	if !validOperationID(result.CommandID) || !validOperationID(result.OperationID) || !validOperationID(result.NodeID) || result.Kind == "" {
		return fmt.Errorf("%w: command, operation, node IDs, and command kind are required", ErrInvalidArgument)
	}
	if result.Attempt < 1 || !validLeaseToken(result.LeaseToken) {
		return fmt.Errorf("%w: a positive attempt and valid lease token are required", ErrInvalidArgument)
	}
	if result.Success {
		if result.State == v1.CommandResultFailed || result.Error != "" {
			return fmt.Errorf("%w: successful result cannot include a failure state or error", ErrInvalidArgument)
		}
	} else if result.State != v1.CommandResultFailed || strings.TrimSpace(result.Error) == "" || len(result.Error) > maxCommandResultError {
		return fmt.Errorf("%w: failed result requires state=failed and a bounded error", ErrInvalidArgument)
	}
	if len(result.Error) > maxCommandResultError {
		return fmt.Errorf("%w: command result error is too long", ErrInvalidArgument)
	}
	tokenHash := leaseTokenHash(result.LeaseToken)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin command result: %w", err)
	}
	defer tx.Rollback()
	var nodeID, operationID, projectID, serviceID, instanceID, kind, commandState string
	var commandPayload []byte
	var attempt int
	var storedTokenHash sql.NullString
	var leaseExpiry, resultReceived sql.NullTime
	err = tx.QueryRowContext(ctx, `
	SELECT node_id, operation_id, project_id, service_id, instance_id, kind, state, payload,
	       attempt, lease_token_hash, lease_expires_at, result_received_at
	FROM porter_control_plane.agent_commands WHERE id = $1 FOR UPDATE`, result.CommandID).
		Scan(&nodeID, &operationID, &projectID, &serviceID, &instanceID, &kind, &commandState,
			&commandPayload, &attempt, &storedTokenHash, &leaseExpiry, &resultReceived)
	if err != nil {
		return notFound("agent command", result.CommandID, err)
	}
	if nodeID != result.NodeID {
		return ErrCommandNodeMismatch
	}
	if operationID != result.OperationID {
		return ErrCommandOperationMismatch
	}
	if kind != string(result.Kind) {
		return ErrCommandKindMismatch
	}
	snapshotID, err := validateSnapshotCommandResult(kind, commandPayload, result.Success, receipt)
	if err != nil {
		return fmt.Errorf("%w: invalid typed command result", ErrInvalidArgument)
	}
	if attempt != result.Attempt {
		return ErrCommandAttemptMismatch
	}
	if !storedTokenHash.Valid || subtle.ConstantTimeCompare([]byte(storedTokenHash.String), []byte(tokenHash)) != 1 {
		return ErrCommandLeaseMismatch
	}
	if commandState == string(v1.AgentCommandSucceeded) || commandState == string(v1.AgentCommandFailed) {
		var storedSuccess bool
		var storedState, storedError string
		var storedArtifactJSON []byte
		err := tx.QueryRowContext(ctx, `
	SELECT success, result_state, error, snapshot_artifact FROM porter_control_plane.agent_command_results
	WHERE command_id = $1 AND attempt = $2 AND lease_token_hash = $3`,
			result.CommandID, result.Attempt, tokenHash).Scan(&storedSuccess, &storedState, &storedError, &storedArtifactJSON)
		if err != nil {
			return ErrCommandResultConflict
		}
		var storedReceipt protocol.SnapshotArtifactReceipt
		if len(storedArtifactJSON) != 0 && string(storedArtifactJSON) != "{}" {
			if err := json.Unmarshal(storedArtifactJSON, &storedReceipt); err != nil {
				return ErrCommandResultConflict
			}
		}
		if storedSuccess == result.Success && storedState == string(result.State) && storedError == result.Error && storedReceipt == receiptValue(receipt) {
			return nil
		}
		return ErrCommandResultConflict
	}
	if commandState != string(v1.AgentCommandLeased) {
		return fmt.Errorf("%w: command is %s", ErrCommandStateConflict, commandState)
	}
	if !leaseExpiry.Valid || !leaseExpiry.Time.After(now) {
		return ErrCommandLeaseExpired
	}
	if !resultStateAllowed(v1.AgentCommandKind(kind), result.Success, result.State) {
		return fmt.Errorf("%w: result state is not valid for this typed command", ErrInvalidArgument)
	}

	operationState := v1.OperationSucceeded
	commandResultState := v1.AgentCommandSucceeded
	if !result.Success {
		operationState = v1.OperationFailed
		commandResultState = v1.AgentCommandFailed
	}
	resultID, err := newID("res")
	if err != nil {
		return err
	}
	artifactJSON, err := json.Marshal(receiptObject(receipt))
	if err != nil {
		return fmt.Errorf("encode path-free snapshot receipt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.agent_command_results
	    (id, command_id, operation_id, project_id, service_id, instance_id, node_id,
	     attempt, lease_token_hash, success, result_state, error, received_at, snapshot_artifact)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		resultID, result.CommandID, result.OperationID, projectID, serviceID, instanceID, nodeID,
		result.Attempt, tokenHash, result.Success, result.State, result.Error, now, string(artifactJSON)); err != nil {
		return fmt.Errorf("insert command result receipt: %w", err)
	}
	commandUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.agent_commands
SET state = $2, result_received_at = $3, updated_at = $3
WHERE id = $1 AND state = 'leased' AND attempt = $4 AND lease_token_hash = $5`,
		result.CommandID, commandResultState, now, result.Attempt, tokenHash)
	if err != nil {
		return fmt.Errorf("complete agent command: %w", err)
	}
	if err := requireOneRow(commandUpdate, "agent command", result.CommandID); err != nil {
		return err
	}
	operationUpdate, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.operations
SET state = $2, error = $3, updated_at = $4
	WHERE id = $1 AND state = 'running'`, result.OperationID, operationState, result.Error, now)
	if err != nil {
		return fmt.Errorf("complete operation: %w", err)
	}
	if err := requireOneRow(operationUpdate, "operation", result.OperationID); err != nil {
		return err
	}
	if err := updateInstanceForCommandResult(ctx, tx, v1.AgentCommandKind(kind), result.Success, commandPayload, instanceID, serviceID, now); err != nil {
		return err
	}
	if err := updateSnapshotMetadataForResult(ctx, tx, v1.AgentCommandKind(kind), result.Success,
		operationID, projectID, serviceID, instanceID, nodeID, snapshotID, commandPayload, receipt, now); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, "operation."+string(operationState), eventRefs{
		projectID: projectID, serviceID: serviceID, instanceID: instanceID,
		fleetNodeID: nodeID, operationID: operationID, commandID: result.CommandID, snapshotID: snapshotID,
	}, map[string]any{"kind": kind, "attempt": result.Attempt, "result_state": result.State, "snapshot_id": snapshotID}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit command result: %w", err)
	}
	return nil
}

// RecordProtocolCommandResult validates the shared v2 receipt before mapping
// it into PostgreSQL persistence. Result fencing still checks persisted node,
// operation, kind, attempt, lease hash, and expiry transactionally.
func (s *Store) RecordProtocolCommandResult(ctx context.Context, result protocol.CommandResultV2) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return s.recordCommandResult(ctx, v1.AgentCommandResult{
		CommandID: result.CommandID, OperationID: result.OperationID, NodeID: result.NodeID,
		Kind: v1.AgentCommandKind(result.Kind), Attempt: result.Attempt, LeaseToken: result.LeaseToken,
		Success: result.Success, State: v1.AgentCommandResultState(result.State), Error: result.Error,
	}, result.SnapshotArtifact)
}

func receiptValue(receipt *protocol.SnapshotArtifactReceipt) protocol.SnapshotArtifactReceipt {
	if receipt == nil {
		return protocol.SnapshotArtifactReceipt{}
	}
	return *receipt
}

func receiptObject(receipt *protocol.SnapshotArtifactReceipt) any {
	if receipt == nil {
		return map[string]any{}
	}
	return receipt
}

func validateSnapshotCommandResult(kind string, payload []byte, success bool, receipt *protocol.SnapshotArtifactReceipt) (string, error) {
	switch kind {
	case string(v1.AgentCommandSnapshotCreate):
		var spec protocol.SnapshotSpec
		if err := json.Unmarshal(payload, &spec); err != nil || spec.Validate() != nil {
			return "", fmt.Errorf("invalid snapshot create command payload")
		}
		if !success {
			if receipt != nil {
				return "", fmt.Errorf("failed snapshot commands cannot include an artifact receipt")
			}
			return spec.SnapshotID, nil
		}
		if receipt == nil || receipt.Validate() != nil || receipt.VMID != spec.VMID ||
			receipt.SnapshotID != spec.SnapshotID || receipt.Consistency != spec.Consistency {
			return "", fmt.Errorf("snapshot create receipt does not match command")
		}
		return spec.SnapshotID, nil
	case string(v1.AgentCommandSnapshotRestore):
		var spec protocol.SnapshotRestoreCommand
		if err := json.Unmarshal(payload, &spec); err != nil || spec.Validate() != nil {
			return "", fmt.Errorf("invalid snapshot restore command payload")
		}
		if !success {
			if receipt != nil {
				return "", fmt.Errorf("failed snapshot commands cannot include an artifact receipt")
			}
			return spec.SnapshotID, nil
		}
		if receipt == nil || receipt.Validate() != nil || receipt.VMID != spec.VMID ||
			receipt.SnapshotID != spec.SnapshotID || receipt.Consistency != spec.Consistency ||
			receipt.ArtifactSHA256 != spec.ArtifactSHA256 || receipt.SizeBytes != spec.SizeBytes {
			return "", fmt.Errorf("snapshot restore receipt does not match command integrity metadata")
		}
		return spec.SnapshotID, nil
	default:
		if receipt != nil {
			return "", fmt.Errorf("artifact receipt is not valid for this command")
		}
		return "", nil
	}
}

func updateSnapshotMetadataForResult(ctx context.Context, tx *sql.Tx, kind v1.AgentCommandKind, success bool,
	operationID, projectID, serviceID, instanceID, nodeID, snapshotID string, commandPayload []byte,
	receipt *protocol.SnapshotArtifactReceipt, now time.Time) error {
	if kind != v1.AgentCommandSnapshotCreate && kind != v1.AgentCommandSnapshotRestore {
		return nil
	}
	var query string
	var args []any
	if kind == v1.AgentCommandSnapshotCreate {
		if success {
			query = `UPDATE porter_control_plane.snapshot_metadata
SET capture_status = 'captured', artifact_sha256 = $1, size_bytes = $2,
    integrity_status = 'verified', integrity_checked_at = $3
WHERE id = $4 AND project_id = $5 AND service_id = $6 AND instance_id = $7
  AND node_affinity_id = $8 AND runtime_vm_id = $9 AND capture_operation_id = $10
  AND capture_status IN ('queued', 'capturing')`
			args = []any{receipt.ArtifactSHA256, receipt.SizeBytes, now, snapshotID, projectID, serviceID,
				instanceID, nodeID, receipt.VMID, operationID}
		} else {
			query = `UPDATE porter_control_plane.snapshot_metadata
SET capture_status = 'failed', artifact_sha256 = NULL, size_bytes = NULL,
    integrity_status = 'not_available', integrity_checked_at = NULL
WHERE id = $1 AND project_id = $2 AND service_id = $3 AND instance_id = $4
  AND node_affinity_id = $5 AND capture_operation_id = $6
  AND capture_status IN ('queued', 'capturing')`
			args = []any{snapshotID, projectID, serviceID, instanceID, nodeID, operationID}
		}
	} else {
		var spec protocol.SnapshotRestoreCommand
		if err := json.Unmarshal(commandPayload, &spec); err != nil || spec.Validate() != nil {
			return fmt.Errorf("%w: invalid persisted snapshot restore payload", ErrInvalidArgument)
		}
		status := "failed"
		if success {
			status = "restored"
		}
		query = `UPDATE porter_control_plane.snapshot_metadata
SET restore_status = $1
WHERE id = $2 AND project_id = $3 AND service_id = $4 AND instance_id = $5
  AND node_affinity_id = $6 AND runtime_vm_id = $7 AND restore_operation_id = $8
  AND capture_status = 'captured' AND restore_status IN ('queued', 'running')`
		args = []any{status, snapshotID, projectID, serviceID, instanceID, nodeID, spec.VMID, operationID}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update snapshot lifecycle metadata: %w", err)
	}
	if err := requireOneRow(result, "snapshot metadata", snapshotID); err != nil {
		return fmt.Errorf("%w: snapshot lifecycle no longer matches the fenced operation", ErrFailedPrecondition)
	}
	return nil
}

func validLeaseToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && len(token) == 43
}

func leaseTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func resultStateAllowed(kind v1.AgentCommandKind, success bool, state v1.AgentCommandResultState) bool {
	switch kind {
	case v1.AgentCommandVMApply, v1.AgentCommandVMDelete, v1.AgentCommandVMStart,
		v1.AgentCommandVMStop, v1.AgentCommandVMRestart, v1.AgentCommandSnapshotCreate,
		v1.AgentCommandSnapshotRestore, v1.AgentCommandVolumeResize:
	default:
		return false
	}
	if !success {
		return state == v1.CommandResultFailed
	}
	switch kind {
	case v1.AgentCommandVMApply, v1.AgentCommandVMStart, v1.AgentCommandVMRestart:
		return state == v1.CommandResultRunning
	case v1.AgentCommandVMDelete:
		return state == v1.CommandResultDeleted
	case v1.AgentCommandVMStop:
		return state == v1.CommandResultStopped
	case v1.AgentCommandSnapshotCreate:
		return state == v1.CommandResultSnapshotReady
	case v1.AgentCommandSnapshotRestore:
		return state == v1.CommandResultRestored
	case v1.AgentCommandVolumeResize:
		return state == v1.CommandResultResized
	default:
		return false
	}
}

func updateInstanceForCommandResult(ctx context.Context, tx *sql.Tx, kind v1.AgentCommandKind, success bool, payload []byte, instanceID, serviceID string, now time.Time) error {
	var state, health string
	var runtimeVMID any
	switch {
	case success && (kind == v1.AgentCommandVMApply || kind == v1.AgentCommandVMStart || kind == v1.AgentCommandVMRestart):
		state, health = string(v1.InstanceRunning), string(v1.HealthUnknown)
		var spec protocol.VMApplySpec
		if err := json.Unmarshal(payload, &spec); err != nil || !validOperationID(spec.VMID) {
			return fmt.Errorf("%w: successful VM result has no persisted runtime identity", ErrInvalidArgument)
		}
		runtimeVMID = spec.VMID
	case success && kind == v1.AgentCommandVMStop:
		state, health = string(v1.InstanceStopped), string(v1.HealthUnknown)
	case success && kind == v1.AgentCommandVMDelete:
		state, health = string(v1.InstanceDeleted), string(v1.HealthUnknown)
	case success && kind == v1.AgentCommandSnapshotRestore:
		state, health = string(v1.InstanceRunning), string(v1.HealthUnknown)
	case !success && kind == v1.AgentCommandSnapshotRestore:
		state, health = string(v1.InstanceFailed), string(v1.HealthUnhealthy)
	case !success && kind == v1.AgentCommandVMApply:
		state, health = string(v1.InstanceFailed), string(v1.HealthUnhealthy)
	default:
		return nil
	}
	result, err := tx.ExecContext(ctx, `
	UPDATE porter_control_plane.instances
	SET state = $1, health = $2, runtime_vm_id = COALESCE($3, runtime_vm_id), updated_at = $4
	WHERE id = $5 AND service_id = $6`, state, health, runtimeVMID, now, instanceID, serviceID)
	if err != nil {
		return fmt.Errorf("apply fenced command result to instance: %w", err)
	}
	return requireOneRow(result, "instance", instanceID)
}
