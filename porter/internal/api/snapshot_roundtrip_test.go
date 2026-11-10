package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

func TestSnapshotAPIQueuesPersistsAndRestoresTypedDiskManifest(t *testing.T) {
	st, httpServer, node, _, replica := newReplicaLifecycleAPIFixture(t)
	completeReplicaApplyThroughAPI(t, httpServer, node, replica)

	const snapshotID = "snapshot-api-roundtrip"
	createBody := doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/snapshots", "test-admin-token", "",
		protocol.SnapshotSpec{VMID: replica.VMID, SnapshotID: snapshotID}, http.StatusAccepted)
	var accepted struct {
		OperationID string `json:"operation_id"`
		SnapshotID  string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(createBody, &accepted); err != nil {
		t.Fatalf("decode snapshot create response: %v", err)
	}
	if accepted.OperationID == "" || accepted.SnapshotID != snapshotID {
		t.Fatalf("unexpected snapshot create response: %+v", accepted)
	}

	commandBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/fleet/commands", node.NodeToken, node.NodeID, nil, http.StatusOK)
	var commands []protocol.Command
	if err := json.Unmarshal(commandBody, &commands); err != nil {
		t.Fatalf("decode snapshot create command: %v", err)
	}
	if len(commands) != 1 || commands[0].Kind != "snapshot.create" || commands[0].OperationID != accepted.OperationID {
		t.Fatalf("unexpected snapshot create command: %+v", commands)
	}
	payload, err := json.Marshal(commands[0].Payload)
	if err != nil {
		t.Fatalf("encode snapshot create command payload: %v", err)
	}
	var snapshotSpec protocol.SnapshotSpec
	if err := json.Unmarshal(payload, &snapshotSpec); err != nil {
		t.Fatalf("decode snapshot create spec: %v", err)
	}
	if snapshotSpec != (protocol.SnapshotSpec{VMID: replica.VMID, SnapshotID: snapshotID}) {
		t.Fatalf("snapshot create payload changed in queue: %+v", snapshotSpec)
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", node.NodeToken, node.NodeID,
		protocol.CommandResult{CommandID: commands[0].ID, OperationID: commands[0].OperationID, NodeID: node.NodeID, Attempt: commands[0].Attempt, Success: true, State: "snapshot_ready"}, http.StatusOK)

	joinToken, err := st.CreateJoinToken(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("create secondary node join token: %v", err)
	}
	otherNode, err := st.Enroll(context.Background(), protocol.EnrollRequest{
		Token: joinToken, Hostname: "snapshot-non-owner", OS: "linux", Arch: "amd64",
		CPUs: 4, MemoryMB: 4096, DiskMB: 8192,
	})
	if err != nil {
		t.Fatalf("enroll secondary node: %v", err)
	}

	diskFiles := []protocol.SnapshotFile{
		{Kind: "rootfs", OriginalPath: fmt.Sprintf("/var/lib/porter/vms/%s/rootfs.ext4", replica.VMID), BackupPath: fmt.Sprintf("/var/lib/porter/vms/%s/%s.rootfs.disk", replica.VMID, snapshotID), SizeBytes: 2048, SHA256: "3333333333333333333333333333333333333333333333333333333333333333"},
		{Kind: "volume", OriginalPath: "/var/lib/porter/volumes/volume-api-roundtrip.ext4", BackupPath: fmt.Sprintf("/var/lib/porter/vms/%s/%s.volume.disk", replica.VMID, snapshotID), SizeBytes: 4096, SHA256: "4444444444444444444444444444444444444444444444444444444444444444"},
	}
	record := protocol.SnapshotRecord{
		ID: snapshotID, VMID: replica.VMID,
		StatePath:   fmt.Sprintf("/var/lib/porter/vms/%s/%s.snap", replica.VMID, snapshotID),
		MemPath:     fmt.Sprintf("/var/lib/porter/vms/%s/%s.mem", replica.VMID, snapshotID),
		SizeBytes:   8192,
		StateSHA256: "1111111111111111111111111111111111111111111111111111111111111111",
		MemSHA256:   "2222222222222222222222222222222222222222222222222222222222222222",
		DiskFiles:   diskFiles,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/snapshot", otherNode.NodeToken, otherNode.NodeID, record, http.StatusForbidden)
	if snapshots, err := st.ListSnapshots(context.Background(), replica.VMID); err != nil || len(snapshots) != 0 {
		t.Fatalf("non-owner node inserted snapshot metadata: snapshots=%+v err=%v", snapshots, err)
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/snapshot", node.NodeToken, node.NodeID, record, http.StatusOK)

	listBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/snapshots?vm_id="+replica.VMID, "test-admin-token", "", nil, http.StatusOK)
	var listed []store.SnapshotRecord
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode persisted snapshots: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != record.ID || listed[0].VMID != record.VMID || listed[0].StatePath != record.StatePath || listed[0].MemPath != record.MemPath || listed[0].StateSHA256 != record.StateSHA256 || listed[0].MemSHA256 != record.MemSHA256 || listed[0].SizeBytes != record.SizeBytes || !reflect.DeepEqual(listed[0].DiskFiles, record.DiskFiles) {
		t.Fatalf("persisted snapshot did not round-trip: got %+v, want %+v", listed, record)
	}

	restoreBody := doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/snapshots/"+snapshotID+"/restore", "test-admin-token", "", nil, http.StatusAccepted)
	var restoreAccepted struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(restoreBody, &restoreAccepted); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if restoreAccepted.OperationID == "" || restoreAccepted.Status != "restore queued" {
		t.Fatalf("unexpected restore response: %+v", restoreAccepted)
	}

	restoreCommandBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/fleet/commands", node.NodeToken, node.NodeID, nil, http.StatusOK)
	if err := json.Unmarshal(restoreCommandBody, &commands); err != nil {
		t.Fatalf("decode restore command: %v", err)
	}
	if len(commands) != 1 || commands[0].Kind != "snapshot.restore" || commands[0].OperationID != restoreAccepted.OperationID {
		t.Fatalf("unexpected restore command: %+v", commands)
	}
	restorePayload, err := json.Marshal(commands[0].Payload)
	if err != nil {
		t.Fatalf("encode restore command payload: %v", err)
	}
	var restoreSpec protocol.SnapshotRestoreSpec
	if err := json.Unmarshal(restorePayload, &restoreSpec); err != nil {
		t.Fatalf("decode restore command spec: %v", err)
	}
	wantRestore := protocol.SnapshotRestoreSpec{
		SnapshotID: record.ID, VMID: record.VMID, StatePath: record.StatePath, MemPath: record.MemPath,
		SizeBytes: record.SizeBytes, StateSHA256: record.StateSHA256, MemSHA256: record.MemSHA256,
		DiskFiles: record.DiskFiles,
	}
	if !reflect.DeepEqual(restoreSpec, wantRestore) {
		t.Fatalf("restore command lost persisted snapshot integrity fields: got %+v, want %+v", restoreSpec, wantRestore)
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", node.NodeToken, node.NodeID,
		protocol.CommandResult{CommandID: commands[0].ID, OperationID: commands[0].OperationID, NodeID: node.NodeID, Attempt: commands[0].Attempt, Success: true, State: "running"}, http.StatusOK)
}
