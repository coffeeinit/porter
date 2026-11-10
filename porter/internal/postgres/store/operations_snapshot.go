package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

// QueueSnapshotCreateOperation queues an idempotent crash-consistent snapshot
// and its metadata in the same transaction as the durable operation/command.
func (s *Store) QueueSnapshotCreateOperation(ctx context.Context, instanceID, consistency, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error) {
	if consistency != protocol.SnapshotCrashConsistent {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("%w: only crash_consistent capture is supported", ErrInvalidArgument)
	}
	if err := validateSnapshotOperationKey(instanceID, idempotencyKey); err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	var nodeID, runtimeVMID sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT node_id, runtime_vm_id FROM porter_control_plane.instances WHERE id = $1`, instanceID).Scan(&nodeID, &runtimeVMID); err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, notFound("instance", instanceID, err)
	}
	if !nodeID.Valid || !runtimeVMID.Valid {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("%w: instance has no assigned node or persisted v2 runtime identity", ErrFailedPrecondition)
	}
	snapshotID := deterministicSnapshotID(instanceID, idempotencyKey)
	spec := &protocol.SnapshotSpec{VMID: runtimeVMID.String, SnapshotID: snapshotID, Consistency: consistency}
	operation, err := s.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: instanceID, NodeID: nodeID.String, IdempotencyKey: idempotencyKey,
		Command: AgentCommandSpec{Kind: v1.AgentCommandSnapshotCreate, SnapshotCreate: spec},
	})
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	snapshot, err := s.GetSnapshotMetadata(ctx, snapshotID)
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("read queued snapshot metadata: %w", err)
	}
	return snapshot, operation, nil
}

// QueueSnapshotRestoreOperation queues a restore only when the snapshot is
// captured, integrity-verified, and still local to the instance's assigned node.
func (s *Store) QueueSnapshotRestoreOperation(ctx context.Context, instanceID, snapshotID, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error) {
	if err := validateSnapshotOperationKey(instanceID, idempotencyKey); err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	if err := validateOperationID("snapshot", snapshotID); err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	var projectID, serviceID, storedInstanceID, captureStatus, consistency string
	var nodeAffinity, runtimeVMID, digest sql.NullString
	var size sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT project_id, service_id, instance_id, capture_status, consistency, node_affinity_id,
       runtime_vm_id, artifact_sha256, size_bytes
FROM porter_control_plane.snapshot_metadata
WHERE id = $1 AND instance_id = $2`, snapshotID, instanceID).Scan(
		&projectID, &serviceID, &storedInstanceID, &captureStatus, &consistency,
		&nodeAffinity, &runtimeVMID, &digest, &size)
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, notFound("snapshot metadata", snapshotID, err)
	}
	if captureStatus != "captured" || consistency != protocol.SnapshotCrashConsistent ||
		!nodeAffinity.Valid || !runtimeVMID.Valid || !digest.Valid || !size.Valid || size.Int64 <= 0 {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("%w: snapshot is not captured with verified local integrity metadata", ErrFailedPrecondition)
	}
	var assignedNode, assignedVM sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT node_id, runtime_vm_id FROM porter_control_plane.instances WHERE id = $1`, instanceID).Scan(&assignedNode, &assignedVM); err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, notFound("instance", instanceID, err)
	}
	if !assignedNode.Valid || !assignedVM.Valid || assignedNode.String != nodeAffinity.String || assignedVM.String != runtimeVMID.String {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("%w: snapshot is not compatible with the instance's current node/runtime identity", ErrFailedPrecondition)
	}
	spec := &protocol.SnapshotRestoreCommand{
		VMID: runtimeVMID.String, SnapshotID: snapshotID, Consistency: consistency,
		ArtifactSHA256: digest.String, SizeBytes: size.Int64,
	}
	operation, err := s.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: storedInstanceID, NodeID: assignedNode.String, IdempotencyKey: idempotencyKey,
		Command: AgentCommandSpec{Kind: v1.AgentCommandSnapshotRestore, SnapshotRestore: spec},
	})
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	snapshot, err := s.GetSnapshotMetadata(ctx, snapshotID)
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("read queued restore metadata: %w", err)
	}
	if snapshot.ProjectID != projectID || snapshot.ServiceID != serviceID {
		return v1.SnapshotMetadata{}, v1.Operation{}, fmt.Errorf("%w: snapshot scope changed during restore queueing", ErrFailedPrecondition)
	}
	return snapshot, operation, nil
}

func validateSnapshotOperationKey(instanceID, idempotencyKey string) error {
	if err := validateOperationID("instance", instanceID); err != nil {
		return err
	}
	if idempotencyKey == "" || len(idempotencyKey) > 128 || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		return fmt.Errorf("%w: idempotency key must contain 1 to 128 non-space characters", ErrInvalidArgument)
	}
	return nil
}

func deterministicSnapshotID(instanceID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(instanceID + "\x00" + idempotencyKey))
	return "snp_" + hex.EncodeToString(digest[:16])
}

func transitionSnapshotForOperation(ctx context.Context, tx *sql.Tx, kind v1.AgentCommandKind, operationID string, leased bool) (string, error) {
	var column, from, to string
	switch kind {
	case v1.AgentCommandSnapshotCreate:
		column, from, to = "capture_operation_id", "queued", "capturing"
	case v1.AgentCommandSnapshotRestore:
		column, from, to = "restore_operation_id", "queued", "running"
	default:
		return "", nil
	}
	if !leased {
		from, to = to, "queued"
	}
	var snapshotID string
	query := `SELECT id FROM porter_control_plane.snapshot_metadata WHERE ` + column + ` = $1 FOR UPDATE`
	if err := tx.QueryRowContext(ctx, query, operationID).Scan(&snapshotID); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: snapshot metadata is missing for operation", ErrFailedPrecondition)
		}
		return "", fmt.Errorf("read snapshot metadata for operation: %w", err)
	}
	statusColumn := "capture_status"
	if kind == v1.AgentCommandSnapshotRestore {
		statusColumn = "restore_status"
	}
	update := `UPDATE porter_control_plane.snapshot_metadata SET ` + statusColumn + ` = $1 WHERE id = $2 AND ` + column + ` = $3 AND ` + statusColumn + ` = $4`
	result, err := tx.ExecContext(ctx, update, to, snapshotID, operationID, from)
	if err != nil {
		return "", fmt.Errorf("transition snapshot status with command lease: %w", err)
	}
	if err := requireOneRow(result, "snapshot metadata", snapshotID); err != nil {
		return "", fmt.Errorf("%w: snapshot metadata is not in the expected lifecycle state", ErrFailedPrecondition)
	}
	return snapshotID, nil
}
