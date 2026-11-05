package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotDigestMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter.db")
	legacyDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE snapshots(
		id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, state_path TEXT NOT NULL,
		mem_path TEXT NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL)`)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create legacy snapshot schema: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open and migrate legacy database: %v", err)
	}
	defer s.Close()

	snap := SnapshotRecord{
		ID: "snap-1", VMID: "vm-1", StatePath: "/var/lib/porter/vm-1/snap-1.snap", MemPath: "/var/lib/porter/vm-1/snap-1.mem",
		StateSHA256: strings.Repeat("a", 64), MemSHA256: strings.Repeat("b", 64), SizeBytes: 123,
	}
	if err := s.RecordSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("record snapshot: %v", err)
	}
	got, ok := s.GetSnapshot(context.Background(), snap.ID)
	if !ok {
		t.Fatal("snapshot not found after insert")
	}
	if got.StateSHA256 != snap.StateSHA256 || got.MemSHA256 != snap.MemSHA256 || got.SizeBytes != snap.SizeBytes {
		t.Fatalf("snapshot integrity fields did not round-trip: %+v", got)
	}
	listed, err := s.ListSnapshots(context.Background(), snap.VMID)
	if err != nil || len(listed) != 1 || listed[0].StateSHA256 != snap.StateSHA256 || listed[0].MemSHA256 != snap.MemSHA256 {
		t.Fatalf("list snapshot hashes: rows=%+v err=%v", listed, err)
	}
}

func TestRecordSnapshotRejectsMissingDigest(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if err := s.RecordSnapshot(context.Background(), SnapshotRecord{ID: "snap-1", VMID: "vm-1"}); err == nil {
		t.Fatal("snapshot without integrity hashes was accepted")
	}
}
