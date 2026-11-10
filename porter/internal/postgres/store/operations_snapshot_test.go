package store

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

func TestDeterministicSnapshotIDIsStableAndSafe(t *testing.T) {
	first := deterministicSnapshotID("ins_1", "capture-key")
	if first != deterministicSnapshotID("ins_1", "capture-key") {
		t.Fatal("same instance/idempotency key produced different snapshot IDs")
	}
	if first == deterministicSnapshotID("ins_1", "different-key") || first == deterministicSnapshotID("ins_2", "capture-key") {
		t.Fatal("different snapshot identity inputs collided in the test")
	}
	if len(first) > 64 || !validOperationID(first) {
		t.Fatalf("snapshot ID is not a safe protocol identity: %q", first)
	}
}

func TestSnapshotCommandResultReceiptBindsIdentityAndIntegrity(t *testing.T) {
	create := protocol.SnapshotSpec{VMID: "vm_1", SnapshotID: "snp_1", Consistency: protocol.SnapshotCrashConsistent}
	createPayload, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &protocol.SnapshotArtifactReceipt{
		VMID: "vm_1", SnapshotID: "snp_1", Consistency: protocol.SnapshotCrashConsistent,
		ArtifactSHA256: strings.Repeat("a", 64), SizeBytes: 2048,
	}
	if got, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotCreate), createPayload, true, receipt); err != nil || got != "snp_1" {
		t.Fatalf("valid create receipt got=%q err=%v", got, err)
	}
	wrongIdentity := *receipt
	wrongIdentity.VMID = "vm_other"
	if _, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotCreate), createPayload, true, &wrongIdentity); err == nil {
		t.Fatal("create receipt with another VM identity was accepted")
	}
	if _, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotCreate), createPayload, true, nil); err == nil {
		t.Fatal("successful capture without an integrity receipt was accepted")
	}
	if _, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotCreate), createPayload, false, receipt); err == nil {
		t.Fatal("failed capture with a success artifact receipt was accepted")
	}

	restore := protocol.SnapshotRestoreCommand{
		VMID: "vm_1", SnapshotID: "snp_1", Consistency: protocol.SnapshotCrashConsistent,
		ArtifactSHA256: receipt.ArtifactSHA256, SizeBytes: receipt.SizeBytes,
	}
	restorePayload, err := json.Marshal(restore)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotRestore), restorePayload, true, receipt); err != nil || got != "snp_1" {
		t.Fatalf("valid restore receipt got=%q err=%v", got, err)
	}
	wrongIntegrity := *receipt
	wrongIntegrity.ArtifactSHA256 = strings.Repeat("b", 64)
	if _, err := validateSnapshotCommandResult(string(v1.AgentCommandSnapshotRestore), restorePayload, true, &wrongIntegrity); err == nil {
		t.Fatal("restore receipt with changed integrity digest was accepted")
	}
}

func TestSnapshotRestoreWireCommandHasNoLocalPaths(t *testing.T) {
	command := protocol.SnapshotRestoreCommand{
		VMID: "vm_1", SnapshotID: "snp_1", Consistency: protocol.SnapshotCrashConsistent,
		ArtifactSHA256: strings.Repeat("0", 64), SizeBytes: 42,
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"path", "socket", "/tmp", "firecracker.sock"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("restore payload contains local path/socket material %q: %s", forbidden, encoded)
		}
	}
}
