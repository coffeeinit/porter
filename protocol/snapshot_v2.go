package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const SnapshotCrashConsistent = "crash_consistent"

// SnapshotRestoreCommand is the controller-to-agent v2 payload. It names only
// the VM and snapshot plus the expected local artifact digest; filesystem paths
// and the Firecracker API socket stay on the agent host.
type SnapshotRestoreCommand struct {
	VMID           string `json:"vm_id"`
	SnapshotID     string `json:"snapshot_id"`
	Consistency    string `json:"consistency"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	SizeBytes      int64  `json:"size_bytes"`
}

// SnapshotArtifactReceipt reports only integrity metadata. In particular, it
// never carries agent-local snapshot paths or a Firecracker socket path.
type SnapshotArtifactReceipt struct {
	VMID           string `json:"vm_id"`
	SnapshotID     string `json:"snapshot_id"`
	Consistency    string `json:"consistency"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	SizeBytes      int64  `json:"size_bytes"`
}

func (spec SnapshotSpec) Validate() error {
	if !safeSnapshotIdentity(spec.VMID) || !safeSnapshotIdentity(spec.SnapshotID) || spec.Consistency != SnapshotCrashConsistent {
		return fmt.Errorf("snapshot create requires safe IDs and consistency=%s", SnapshotCrashConsistent)
	}
	return nil
}

func (spec SnapshotRestoreCommand) Validate() error {
	if !safeSnapshotIdentity(spec.VMID) || !safeSnapshotIdentity(spec.SnapshotID) ||
		spec.Consistency != SnapshotCrashConsistent || !validSHA256Hex(spec.ArtifactSHA256) || spec.SizeBytes <= 0 {
		return fmt.Errorf("snapshot restore requires safe IDs, crash-consistent metadata, digest, and positive size")
	}
	return nil
}

func (receipt SnapshotArtifactReceipt) Validate() error {
	if !safeSnapshotIdentity(receipt.VMID) || !safeSnapshotIdentity(receipt.SnapshotID) ||
		receipt.Consistency != SnapshotCrashConsistent || !validSHA256Hex(receipt.ArtifactSHA256) || receipt.SizeBytes <= 0 {
		return fmt.Errorf("snapshot artifact receipt is incomplete or invalid")
	}
	return nil
}

// SnapshotArtifactReceiptFor produces a stable aggregate digest over the local
// record's state, memory, and disk checksums without including agent-local paths.
func SnapshotArtifactReceiptFor(record SnapshotRecord) (SnapshotArtifactReceipt, error) {
	if !safeSnapshotIdentity(record.VMID) || !safeSnapshotIdentity(record.ID) ||
		!validSHA256Hex(record.StateSHA256) || !validSHA256Hex(record.MemSHA256) || record.SizeBytes <= 0 {
		return SnapshotArtifactReceipt{}, fmt.Errorf("snapshot record identity, component digests, and size are required")
	}
	type diskDigest struct {
		Kind      string `json:"kind"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	}
	disks := make([]diskDigest, 0, len(record.DiskFiles))
	seen := map[string]bool{}
	for _, file := range record.DiskFiles {
		if (file.Kind != "rootfs" && file.Kind != "volume") || seen[file.Kind] || file.SizeBytes <= 0 || !validSHA256Hex(file.SHA256) {
			return SnapshotArtifactReceipt{}, fmt.Errorf("snapshot record contains invalid disk integrity metadata")
		}
		seen[file.Kind] = true
		disks = append(disks, diskDigest{Kind: file.Kind, SizeBytes: file.SizeBytes, SHA256: file.SHA256})
	}
	if !seen["rootfs"] {
		return SnapshotArtifactReceipt{}, fmt.Errorf("snapshot record is missing its rootfs integrity metadata")
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Kind < disks[j].Kind })
	manifest := struct {
		Version     int          `json:"version"`
		VMID        string       `json:"vm_id"`
		SnapshotID  string       `json:"snapshot_id"`
		Consistency string       `json:"consistency"`
		StateSHA256 string       `json:"state_sha256"`
		MemSHA256   string       `json:"mem_sha256"`
		SizeBytes   int64        `json:"size_bytes"`
		Disks       []diskDigest `json:"disks"`
	}{1, record.VMID, record.ID, SnapshotCrashConsistent, record.StateSHA256, record.MemSHA256, record.SizeBytes, disks}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return SnapshotArtifactReceipt{}, fmt.Errorf("encode snapshot integrity manifest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return SnapshotArtifactReceipt{
		VMID: record.VMID, SnapshotID: record.ID, Consistency: SnapshotCrashConsistent,
		ArtifactSHA256: hex.EncodeToString(digest[:]), SizeBytes: record.SizeBytes,
	}, nil
}

func validateSnapshotCommandPayload(kind string, payload []byte) error {
	var value any
	switch kind {
	case "snapshot.create":
		value = &SnapshotSpec{}
	case "snapshot.restore":
		value = &SnapshotRestoreCommand{}
	default:
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode typed %s payload: %w", kind, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("typed %s payload contains trailing data", kind)
	}
	switch spec := value.(type) {
	case *SnapshotSpec:
		return spec.Validate()
	case *SnapshotRestoreCommand:
		return spec.Validate()
	default:
		return fmt.Errorf("unsupported snapshot command payload")
	}
}

func validateSnapshotResultForCommand(command CommandV2, result CommandResultV2) error {
	if !result.Success || (command.Kind != "snapshot.create" && command.Kind != "snapshot.restore") {
		return nil
	}
	if result.SnapshotArtifact == nil {
		return fmt.Errorf("successful snapshot command requires a path-free artifact receipt")
	}
	var expected SnapshotArtifactReceipt
	switch command.Kind {
	case "snapshot.create":
		var spec SnapshotSpec
		if err := json.Unmarshal(command.Payload, &spec); err != nil {
			return err
		}
		if err := spec.Validate(); err != nil {
			return err
		}
		expected.VMID, expected.SnapshotID, expected.Consistency = spec.VMID, spec.SnapshotID, spec.Consistency
	case "snapshot.restore":
		var spec SnapshotRestoreCommand
		if err := json.Unmarshal(command.Payload, &spec); err != nil {
			return err
		}
		if err := spec.Validate(); err != nil {
			return err
		}
		expected = SnapshotArtifactReceipt{
			VMID: spec.VMID, SnapshotID: spec.SnapshotID, Consistency: spec.Consistency,
			ArtifactSHA256: spec.ArtifactSHA256, SizeBytes: spec.SizeBytes,
		}
	}
	actual := *result.SnapshotArtifact
	if err := actual.Validate(); err != nil {
		return err
	}
	if actual.VMID != expected.VMID || actual.SnapshotID != expected.SnapshotID || actual.Consistency != expected.Consistency {
		return fmt.Errorf("snapshot artifact receipt does not match command identity")
	}
	if command.Kind == "snapshot.restore" && (actual.ArtifactSHA256 != expected.ArtifactSHA256 || actual.SizeBytes != expected.SizeBytes) {
		return fmt.Errorf("snapshot restore receipt does not match expected integrity metadata")
	}
	return nil
}

func safeSnapshotIdentity(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
