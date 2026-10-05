package store

import (
	"context"
	"log"
	"time"
)

// This file persists storage.Snapshot pairs and storage.Backup descriptors
// as ObjectStore references (never local sidecar paths).

// SnapshotRow is one stored snapshot pair descriptor.
type SnapshotRow struct {
	ID          string
	VMID        string
	Type        string
	StateObject string
	MemObject   string
	SizeBytes   int64
	Verified    bool
	CreatedAt   time.Time
}

// RecordSnapshot inserts a snapshot descriptor and returns its id.
func (s *Store) RecordSnapshot(vmID, snapType, stateObj, memObj string, sizeBytes int64) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO snapshots (vm_id, snap_type, state_object, mem_object, size_bytes)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id::text`,
		vmID, snapType, stateObj, memObj, sizeBytes).Scan(&id)
	if err != nil {
		log.Printf("store: record snapshot: %v", err)
	}
	return id, err
}

// ListSnapshots returns newest-first snapshot descriptors for a VM.
func (s *Store) ListSnapshots(vmID string) []SnapshotRow {
	return s.listSnapshots(`SELECT id::text, vm_id, snap_type, state_object, mem_object,
		size_bytes, verified, created_at FROM snapshots
		WHERE vm_id = $1 ORDER BY created_at DESC LIMIT 100`, vmID)
}

// MarkSnapshotVerified flips a snapshot trusted after restore verification.
func (s *Store) MarkSnapshotVerified(id string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE snapshots SET verified = TRUE WHERE id::text = $1`, id)
	if err != nil {
		log.Printf("store: verify snapshot: %v", err)
	}
	return err
}

func (s *Store) listSnapshots(q string, args ...any) []SnapshotRow {
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		log.Printf("store: list snapshots: %v", err)
		return nil
	}
	defer rows.Close()
	out := []SnapshotRow{}
	for rows.Next() {
		var r SnapshotRow
		if err := rows.Scan(&r.ID, &r.VMID, &r.Type, &r.StateObject, &r.MemObject,
			&r.SizeBytes, &r.Verified, &r.CreatedAt); err != nil {
			log.Printf("store: scan snapshot: %v", err)
			return out
		}
		out = append(out, r)
	}
	return out
}

// BackupRow is one stored backup descriptor.
type BackupRow struct {
	ID         string
	WorkloadID string
	ObjectPath string
	SizeBytes  int64
	SHA256     string
	Trigger    string
	Verified   bool
	CreatedAt  time.Time
}

// RecordBackup inserts a backup descriptor and returns its id.
func (s *Store) RecordBackup(workloadID, objectPath string, sizeBytes int64, sha256, trigger string) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO backups (workload_id, object_path, size_bytes, sha256, trigger)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id::text`,
		workloadID, objectPath, sizeBytes, sha256, trigger).Scan(&id)
	if err != nil {
		log.Printf("store: record backup: %v", err)
	}
	return id, err
}

// ListBackups returns newest-first backup descriptors for a workload.
func (s *Store) ListBackups(workloadID string) []BackupRow {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id::text, workload_id, object_path, size_bytes, sha256, trigger,
		verified, created_at FROM backups WHERE workload_id = $1
		ORDER BY created_at DESC LIMIT 100`, workloadID)
	if err != nil {
		log.Printf("store: list backups: %v", err)
		return nil
	}
	defer rows.Close()
	out := []BackupRow{}
	for rows.Next() {
		var r BackupRow
		if err := rows.Scan(&r.ID, &r.WorkloadID, &r.ObjectPath, &r.SizeBytes,
			&r.SHA256, &r.Trigger, &r.Verified, &r.CreatedAt); err != nil {
			log.Printf("store: scan backup: %v", err)
			return out
		}
		out = append(out, r)
	}
	return out
}

// SnapshotRetention is the default per-VM snapshot history depth.
const SnapshotRetention = 3

// PruneSnapshots deletes snapshots beyond the newest keep rows for a VM.
// Files on disk/ObjectStore are NOT removed here — the snapshot dir sweeper
// (host, offline) reclaims unreferenced files. Returns rows removed.
func (s *Store) PruneSnapshots(vmID string, keep int) int64 {
	if keep < 1 {
		keep = SnapshotRetention
	}
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM snapshots WHERE vm_id = $1 AND id NOT IN (
		   SELECT id FROM snapshots WHERE vm_id = $1
		   ORDER BY created_at DESC LIMIT $2
		 )`, vmID, keep)
	if err != nil {
		log.Printf("store: prune snapshots: %v", err)
		return 0
	}
	return res.RowsAffected()
}

// VerifyNewestSnapshot marks the newest snapshot row for vmID verified when
// it matches the restored paths (restore proved the pair boots).
func (s *Store) VerifyNewestSnapshot(vmID, stateObj, memObj string) {
	rows := s.ListSnapshots(vmID)
	if len(rows) == 0 {
		return
	}
	if rows[0].StateObject == stateObj && rows[0].MemObject == memObj {
		_ = s.MarkSnapshotVerified(rows[0].ID)
	}
}
