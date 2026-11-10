package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validSnapshotRecord() SnapshotRecord {
	return SnapshotRecord{
		ID: "snp_123", VMID: "vm_123",
		StatePath: "/var/lib/porter/vms/vm_123/snp_123.snap",
		MemPath:   "/var/lib/porter/vms/vm_123/snp_123.mem",
		SizeBytes: 8192, StateSHA256: strings.Repeat("a", 64), MemSHA256: strings.Repeat("b", 64),
		DiskFiles: []SnapshotFile{
			{Kind: "volume", OriginalPath: "/var/lib/porter/volumes/data.ext4", BackupPath: "/var/lib/porter/vms/vm_123/snp_123.volume.disk", SizeBytes: 4096, SHA256: strings.Repeat("d", 64)},
			{Kind: "rootfs", OriginalPath: "/var/lib/porter/rootfs.ext4", BackupPath: "/var/lib/porter/vms/vm_123/snp_123.rootfs.disk", SizeBytes: 4096, SHA256: strings.Repeat("c", 64)},
		},
	}
}

func TestSnapshotArtifactDigestIsStableAndExcludesLocalPaths(t *testing.T) {
	record := validSnapshotRecord()
	first, err := SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatal(err)
	}
	record.StatePath = "/different/agent/root/state.snap"
	record.MemPath = "/different/agent/root/mem"
	record.DiskFiles[0].OriginalPath = "/mnt/another-host/data.ext4"
	record.DiskFiles[1], record.DiskFiles[0] = record.DiskFiles[0], record.DiskFiles[1]
	second, err := SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("local paths or disk ordering changed stable receipt: first=%+v second=%+v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"path", "socket", "/var/lib", "/mnt/"} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Fatalf("receipt contains path/socket material %q: %s", forbidden, encoded)
		}
	}
}

func TestSnapshotArtifactReceiptRejectsInvalidComponentIntegrity(t *testing.T) {
	record := validSnapshotRecord()
	record.DiskFiles[0].SHA256 = "not-a-digest"
	if _, err := SnapshotArtifactReceiptFor(record); err == nil {
		t.Fatal("snapshot with invalid disk digest was accepted")
	}
	record = validSnapshotRecord()
	record.DiskFiles = record.DiskFiles[:1]
	if _, err := SnapshotArtifactReceiptFor(record); err == nil {
		t.Fatal("snapshot without rootfs integrity metadata was accepted")
	}
}

func TestV2SnapshotCreateRestorePayloadAndReceiptValidation(t *testing.T) {
	now := time.Now().UTC()
	leaseToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	record := validSnapshotRecord()
	receipt, err := SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatal(err)
	}
	createPayload, _ := json.Marshal(SnapshotSpec{VMID: record.VMID, SnapshotID: record.ID, Consistency: SnapshotCrashConsistent})
	create := CommandV2{
		ProtocolVersion: VersionV2, ID: "cmd_create", NodeID: "node_1", OperationID: "op_create", Attempt: 1,
		Kind: "snapshot.create", LeaseToken: leaseToken, LeaseExpiresAt: now.Add(time.Minute),
		Payload: createPayload, State: "leased", CreatedAt: now,
	}
	if err := create.ValidateAt(now); err != nil {
		t.Fatalf("valid snapshot.create command rejected: %v", err)
	}
	createResult := CommandResultV2{
		ProtocolVersion: VersionV2, CommandID: create.ID, OperationID: create.OperationID, NodeID: create.NodeID,
		Attempt: create.Attempt, Kind: create.Kind, LeaseToken: leaseToken, Success: true,
		State: "snapshot_ready", SnapshotArtifact: &receipt,
	}
	if err := createResult.ValidateForAt(create, now); err != nil {
		t.Fatalf("valid capture receipt rejected: %v", err)
	}
	missingReceipt := createResult
	missingReceipt.SnapshotArtifact = nil
	if err := missingReceipt.ValidateForAt(create, now); !errors.Is(err, ErrInvalidLeasedMessage) {
		t.Fatalf("capture without receipt error=%v, want invalid leased message", err)
	}
	create.Payload = json.RawMessage(`{"vm_id":"vm_123","snapshot_id":"snp_123","consistency":"crash_consistent","state_path":"/tmp/private"}`)
	if err := create.ValidateAt(now); !errors.Is(err, ErrInvalidLeasedMessage) {
		t.Fatalf("path-bearing capture command error=%v, want invalid leased message", err)
	}

	restorePayload, _ := json.Marshal(SnapshotRestoreCommand{
		VMID: record.VMID, SnapshotID: record.ID, Consistency: SnapshotCrashConsistent,
		ArtifactSHA256: receipt.ArtifactSHA256, SizeBytes: receipt.SizeBytes,
	})
	restore := CommandV2{
		ProtocolVersion: VersionV2, ID: "cmd_restore", NodeID: "node_1", OperationID: "op_restore", Attempt: 2,
		Kind: "snapshot.restore", LeaseToken: leaseToken, LeaseExpiresAt: now.Add(time.Minute),
		Payload: restorePayload, State: "leased", CreatedAt: now,
	}
	if err := restore.ValidateAt(now); err != nil {
		t.Fatalf("valid snapshot.restore command rejected: %v", err)
	}
	restoreResult := CommandResultV2{
		ProtocolVersion: VersionV2, CommandID: restore.ID, OperationID: restore.OperationID, NodeID: restore.NodeID,
		Attempt: restore.Attempt, Kind: restore.Kind, LeaseToken: leaseToken, Success: true,
		State: "restored", SnapshotArtifact: &receipt,
	}
	if err := restoreResult.ValidateForAt(restore, now); err != nil {
		t.Fatalf("valid restore receipt rejected: %v", err)
	}
	wrongReceipt := restoreResult
	changed := receipt
	changed.ArtifactSHA256 = strings.Repeat("e", 64)
	wrongReceipt.SnapshotArtifact = &changed
	if err := wrongReceipt.ValidateForAt(restore, now); !errors.Is(err, ErrCommandIdentity) {
		t.Fatalf("restore receipt with different digest error=%v, want command identity error", err)
	}
}
