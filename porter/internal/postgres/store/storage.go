package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

const maxVolumeSizeMiB int64 = 16 * 1024 * 1024

const volumeMetadataSelect = `
SELECT id, project_id, service_id, name, size_mib, mount_path, access_mode, state,
       node_affinity_id, integrity_status, integrity_sha256, integrity_checked_at,
       created_at, updated_at
FROM porter_control_plane.volume_metadata`

const snapshotMetadataSelect = `
SELECT id, project_id, service_id, instance_id, consistency, capture_status,
       capture_operation_id, restore_status, restore_operation_id,
       node_affinity_id, artifact_sha256, size_bytes, integrity_status,
       integrity_checked_at, created_at
FROM porter_control_plane.snapshot_metadata`

const backupMetadataSelect = `
SELECT id, project_id, service_id, instance_id, snapshot_id, consistency, state,
       off_host_status, node_affinity_id, artifact_sha256, size_bytes,
       integrity_status, integrity_checked_at, created_at
FROM porter_control_plane.backup_metadata`

func normalizeMountPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 4096 || value[0] != '/' || strings.ContainsAny(value, "?#\\%\x00\r\n\t ") {
		return "", fmt.Errorf("%w: mount path must be a safe absolute path", ErrInvalidArgument)
	}
	if value != "/" {
		if strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
			return "", fmt.Errorf("%w: mount path must use canonical separators", ErrInvalidArgument)
		}
		for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
			if segment == "" || segment == "." || segment == ".." {
				return "", fmt.Errorf("%w: mount path contains an unsafe segment", ErrInvalidArgument)
			}
			for i := 0; i < len(segment); i++ {
				c := segment[i]
				if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '.' && c != '_' && c != '-' {
					return "", fmt.Errorf("%w: mount path contains an unsupported character", ErrInvalidArgument)
				}
			}
		}
	}
	return value, nil
}

func (s *Store) CreateVolume(ctx context.Context, projectID, serviceID string, request v1.CreateVolumeRequest) (v1.Volume, error) {
	request.Name = strings.TrimSpace(request.Name)
	if err := validateName("volume", request.Name); err != nil {
		return v1.Volume{}, err
	}
	if request.SizeMiB < 1 || request.SizeMiB > maxVolumeSizeMiB {
		return v1.Volume{}, fmt.Errorf("%w: volume size must be between 1 MiB and 16 TiB", ErrInvalidArgument)
	}
	mountPath, err := normalizeMountPath(request.MountPath)
	if err != nil {
		return v1.Volume{}, err
	}
	if request.AccessMode == "" {
		request.AccessMode = v1.VolumeSingleWriter
	}
	if request.AccessMode != v1.VolumeSingleWriter {
		return v1.Volume{}, fmt.Errorf("%w: only single_writer volume metadata is supported", ErrInvalidArgument)
	}
	request.NodeAffinityID = strings.TrimSpace(request.NodeAffinityID)
	if len(request.NodeAffinityID) > 128 {
		return v1.Volume{}, fmt.Errorf("%w: node affinity ID is too long", ErrInvalidArgument)
	}
	id, err := newID("vol")
	if err != nil {
		return v1.Volume{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Volume{}, fmt.Errorf("begin volume metadata creation: %w", err)
	}
	defer tx.Rollback()
	var projectState v1.ProjectState
	var serviceState v1.ServiceState
	if err := tx.QueryRowContext(ctx, `
SELECT p.state, s.state
FROM porter_control_plane.projects p
JOIN porter_control_plane.services s ON s.project_id = p.id
WHERE p.id = $1 AND s.id = $2
FOR UPDATE OF p, s`, projectID, serviceID).Scan(&projectState, &serviceState); err != nil {
		if err == sql.ErrNoRows {
			return v1.Volume{}, fmt.Errorf("%w: service is not in project", ErrNotFound)
		}
		return v1.Volume{}, fmt.Errorf("lock volume project/service: %w", err)
	}
	if projectState != v1.ProjectActive || serviceState != v1.ServiceActive {
		return v1.Volume{}, fmt.Errorf("%w: volume metadata requires an active project and service", ErrFailedPrecondition)
	}
	if request.NodeAffinityID != "" {
		var nodeID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR KEY SHARE`, request.NodeAffinityID).Scan(&nodeID); err != nil {
			if err == sql.ErrNoRows {
				return v1.Volume{}, fmt.Errorf("%w: fleet node %q", ErrNotFound, request.NodeAffinityID)
			}
			return v1.Volume{}, fmt.Errorf("read volume node affinity: %w", err)
		}
	}
	volume, err := scanVolume(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.volume_metadata
    (id, project_id, service_id, name, size_mib, mount_path, access_mode, node_affinity_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, project_id, service_id, name, size_mib, mount_path, access_mode, state,
          node_affinity_id, integrity_status, integrity_sha256, integrity_checked_at,
          created_at, updated_at`, id, projectID, serviceID, request.Name, request.SizeMiB,
		mountPath, request.AccessMode, nullableID(request.NodeAffinityID)))
	if err != nil {
		return v1.Volume{}, fmt.Errorf("create volume metadata: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "volume.metadata_created", eventRefs{
		projectID: projectID, serviceID: serviceID, volumeID: volume.ID,
	}, map[string]any{"name": volume.Name, "size_mib": volume.SizeMiB, "access_mode": volume.AccessMode, "node_affinity_id": request.NodeAffinityID}); err != nil {
		return v1.Volume{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Volume{}, fmt.Errorf("commit volume metadata creation: %w", err)
	}
	return volume, nil
}

func (s *Store) GetVolume(ctx context.Context, id string) (v1.Volume, error) {
	volume, err := scanVolume(s.db.QueryRowContext(ctx, volumeMetadataSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.Volume{}, notFound("volume", id, err)
	}
	return volume, nil
}

func (s *Store) ListVolumesPage(ctx context.Context, projectID, serviceID string, limit int, after *TimeCursor) ([]v1.Volume, error) {
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
	rows, err := s.db.QueryContext(ctx, volumeMetadataSelect+`
WHERE project_id = $1
  AND ($2::text IS NULL OR service_id = $2::text)
  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3::timestamptz, $4::text))
ORDER BY created_at, id
LIMIT $5`, projectID, nullableID(serviceID), createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list volume metadata page: %w", err)
	}
	return scanRows(rows, "volume metadata", scanVolume)
}

func (s *Store) CreateSnapshotMetadata(ctx context.Context, instanceID, consistency string) (v1.SnapshotMetadata, error) {
	if consistency != v1.SnapshotCrashConsistent {
		return v1.SnapshotMetadata{}, fmt.Errorf("%w: only crash_consistent snapshot metadata is supported", ErrInvalidArgument)
	}
	id, err := newID("snp")
	if err != nil {
		return v1.SnapshotMetadata{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.SnapshotMetadata{}, fmt.Errorf("begin snapshot metadata creation: %w", err)
	}
	defer tx.Rollback()
	var projectID, serviceID string
	var nodeID sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT s.project_id, i.service_id, i.node_id
FROM porter_control_plane.instances i
JOIN porter_control_plane.services s ON s.id = i.service_id
WHERE i.id = $1
FOR UPDATE OF i`, instanceID).Scan(&projectID, &serviceID, &nodeID); err != nil {
		return v1.SnapshotMetadata{}, notFound("instance", instanceID, err)
	}
	var nodeAffinity any
	if nodeID.Valid {
		nodeAffinity = nodeID.String
	}
	snapshot, err := scanSnapshotMetadata(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.snapshot_metadata (id, project_id, service_id, instance_id, consistency, node_affinity_id)
VALUES ($1, $2, $3, $4, $5, $6)
	RETURNING id, project_id, service_id, instance_id, consistency, capture_status,
	          capture_operation_id, restore_status, restore_operation_id,
	          node_affinity_id, artifact_sha256, size_bytes, integrity_status,
	          integrity_checked_at, created_at`, id, projectID, serviceID, instanceID,
		consistency, nodeAffinity))
	if err != nil {
		return v1.SnapshotMetadata{}, fmt.Errorf("create snapshot metadata: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "snapshot.metadata_created", eventRefs{
		projectID: projectID, serviceID: serviceID, instanceID: instanceID, snapshotID: snapshot.ID,
	}, map[string]any{"consistency": snapshot.Consistency, "capture_status": snapshot.CaptureStatus, "integrity_status": snapshot.IntegrityStatus}); err != nil {
		return v1.SnapshotMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.SnapshotMetadata{}, fmt.Errorf("commit snapshot metadata creation: %w", err)
	}
	return snapshot, nil
}

func (s *Store) GetSnapshotMetadata(ctx context.Context, id string) (v1.SnapshotMetadata, error) {
	snapshot, err := scanSnapshotMetadata(s.db.QueryRowContext(ctx, snapshotMetadataSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.SnapshotMetadata{}, notFound("snapshot metadata", id, err)
	}
	return snapshot, nil
}

func (s *Store) ListSnapshotMetadataPage(ctx context.Context, projectID, instanceID string, limit int, after *TimeCursor) ([]v1.SnapshotMetadata, error) {
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
	rows, err := s.db.QueryContext(ctx, snapshotMetadataSelect+`
WHERE project_id = $1
  AND ($2::text IS NULL OR instance_id = $2::text)
  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3::timestamptz, $4::text))
ORDER BY created_at, id
LIMIT $5`, projectID, nullableID(instanceID), createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list snapshot metadata page: %w", err)
	}
	return scanRows(rows, "snapshot metadata", scanSnapshotMetadata)
}

func (s *Store) CreateBackupMetadata(ctx context.Context, snapshotID, consistency string) (v1.BackupMetadata, error) {
	if consistency != v1.SnapshotCrashConsistent {
		return v1.BackupMetadata{}, fmt.Errorf("%w: only crash_consistent backup metadata is supported", ErrInvalidArgument)
	}
	id, err := newID("bkp")
	if err != nil {
		return v1.BackupMetadata{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.BackupMetadata{}, fmt.Errorf("begin backup metadata creation: %w", err)
	}
	defer tx.Rollback()
	var projectID, serviceID, instanceID string
	var nodeID sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT project_id, service_id, instance_id, node_affinity_id
FROM porter_control_plane.snapshot_metadata
WHERE id = $1
FOR UPDATE`, snapshotID).Scan(&projectID, &serviceID, &instanceID, &nodeID); err != nil {
		return v1.BackupMetadata{}, notFound("snapshot metadata", snapshotID, err)
	}
	var nodeAffinity any
	if nodeID.Valid {
		nodeAffinity = nodeID.String
	}
	backup, err := scanBackupMetadata(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.backup_metadata
    (id, project_id, service_id, instance_id, snapshot_id, consistency, node_affinity_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, project_id, service_id, instance_id, snapshot_id, consistency, state,
          off_host_status, node_affinity_id, artifact_sha256, size_bytes,
          integrity_status, integrity_checked_at, created_at`, id, projectID, serviceID,
		instanceID, snapshotID, consistency, nodeAffinity))
	if err != nil {
		return v1.BackupMetadata{}, fmt.Errorf("create backup metadata: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "backup.metadata_created", eventRefs{
		projectID: projectID, serviceID: serviceID, instanceID: instanceID,
		snapshotID: snapshotID, backupID: backup.ID,
	}, map[string]any{"consistency": backup.Consistency, "state": backup.State, "off_host_status": backup.OffHostStatus, "integrity_status": backup.IntegrityStatus}); err != nil {
		return v1.BackupMetadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.BackupMetadata{}, fmt.Errorf("commit backup metadata creation: %w", err)
	}
	return backup, nil
}

func (s *Store) GetBackupMetadata(ctx context.Context, id string) (v1.BackupMetadata, error) {
	backup, err := scanBackupMetadata(s.db.QueryRowContext(ctx, backupMetadataSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.BackupMetadata{}, notFound("backup metadata", id, err)
	}
	return backup, nil
}

func (s *Store) ListBackupMetadataPage(ctx context.Context, projectID, snapshotID string, limit int, after *TimeCursor) ([]v1.BackupMetadata, error) {
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
	rows, err := s.db.QueryContext(ctx, backupMetadataSelect+`
WHERE project_id = $1
  AND ($2::text IS NULL OR snapshot_id = $2::text)
  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3::timestamptz, $4::text))
ORDER BY created_at, id
LIMIT $5`, projectID, nullableID(snapshotID), createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list backup metadata page: %w", err)
	}
	return scanRows(rows, "backup metadata", scanBackupMetadata)
}

func (s *Store) ListProjectInstancesPage(ctx context.Context, projectID string, limit int, after *TimeCursor) ([]v1.Instance, error) {
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
SELECT i.id, i.service_id, i.release_id, i.ordinal, i.runtime, i.state, i.health,
       i.node_id, i.created_at, i.updated_at
FROM porter_control_plane.instances i
JOIN porter_control_plane.services s ON s.id = i.service_id
WHERE s.project_id = $1
  AND ($2::timestamptz IS NULL OR (i.created_at, i.id) > ($2::timestamptz, $3::text))
ORDER BY i.created_at, i.id
LIMIT $4`, projectID, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list project instance page: %w", err)
	}
	return scanRows(rows, "project instance", scanInstance)
}

func scanVolume(row rowScanner) (v1.Volume, error) {
	var volume v1.Volume
	var nodeID, digest sql.NullString
	var checked sql.NullTime
	err := row.Scan(&volume.ID, &volume.ProjectID, &volume.ServiceID, &volume.Name,
		&volume.SizeMiB, &volume.MountPath, &volume.AccessMode, &volume.State,
		&nodeID, &volume.IntegrityStatus, &digest, &checked, &volume.CreatedAt, &volume.UpdatedAt)
	if err != nil {
		return v1.Volume{}, err
	}
	if nodeID.Valid {
		volume.NodeAffinityID = &nodeID.String
	}
	if digest.Valid {
		volume.IntegritySHA256 = &digest.String
	}
	if checked.Valid {
		volume.IntegrityCheckedAt = &checked.Time
	}
	return volume, nil
}

func scanSnapshotMetadata(row rowScanner) (v1.SnapshotMetadata, error) {
	var snapshot v1.SnapshotMetadata
	var captureOperationID, restoreOperationID, nodeID, digest sql.NullString
	var size sql.NullInt64
	var checked sql.NullTime
	err := row.Scan(&snapshot.ID, &snapshot.ProjectID, &snapshot.ServiceID, &snapshot.InstanceID,
		&snapshot.Consistency, &snapshot.CaptureStatus, &captureOperationID, &snapshot.RestoreStatus,
		&restoreOperationID, &nodeID, &digest, &size,
		&snapshot.IntegrityStatus, &checked, &snapshot.CreatedAt)
	if err != nil {
		return v1.SnapshotMetadata{}, err
	}
	if nodeID.Valid {
		snapshot.NodeAffinityID = &nodeID.String
	}
	if captureOperationID.Valid {
		snapshot.CaptureOperationID = &captureOperationID.String
	}
	if restoreOperationID.Valid {
		snapshot.RestoreOperationID = &restoreOperationID.String
	}
	if digest.Valid {
		snapshot.ArtifactSHA256 = &digest.String
	}
	if size.Valid {
		snapshot.SizeBytes = &size.Int64
	}
	if checked.Valid {
		snapshot.IntegrityCheckedAt = &checked.Time
	}
	return snapshot, nil
}

func scanBackupMetadata(row rowScanner) (v1.BackupMetadata, error) {
	var backup v1.BackupMetadata
	var nodeID, digest sql.NullString
	var size sql.NullInt64
	var checked sql.NullTime
	err := row.Scan(&backup.ID, &backup.ProjectID, &backup.ServiceID, &backup.InstanceID,
		&backup.SnapshotID, &backup.Consistency, &backup.State, &backup.OffHostStatus,
		&nodeID, &digest, &size, &backup.IntegrityStatus, &checked, &backup.CreatedAt)
	if err != nil {
		return v1.BackupMetadata{}, err
	}
	if nodeID.Valid {
		backup.NodeAffinityID = &nodeID.String
	}
	if digest.Valid {
		backup.ArtifactSHA256 = &digest.String
	}
	if size.Valid {
		backup.SizeBytes = &size.Int64
	}
	if checked.Valid {
		backup.IntegrityCheckedAt = &checked.Time
	}
	return backup, nil
}
