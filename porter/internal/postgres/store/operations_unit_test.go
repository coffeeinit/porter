package store

import (
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

func validApplySpec() *protocol.VMApplySpec {
	return &protocol.VMApplySpec{
		VMID: "vm_1", ReplicaID: "ins_1", ProjectID: "prj_1", DeploymentID: "rel_1",
		Image: "sha256:image", KernelPath: "/var/lib/porter/vmlinux", RootfsPath: "/var/lib/porter/rootfs.ext4",
		VCPUs: 2, MemoryMB: 1024, DiskMB: 8192,
	}
}

func TestAgentCommandSpecIsTypedAndRejectsArbitraryKinds(t *testing.T) {
	payload, err := (AgentCommandSpec{Kind: v1.AgentCommandVMApply, VMApply: validApplySpec()}).marshalPayload()
	if err != nil || !strings.Contains(string(payload), `"vm_id":"vm_1"`) {
		t.Fatalf("valid typed VM command payload=%s err=%v", payload, err)
	}
	for name, spec := range map[string]AgentCommandSpec{
		"arbitrary shell":    {Kind: v1.AgentCommandKind("shell.exec"), VMApply: validApplySpec()},
		"missing payload":    {Kind: v1.AgentCommandVMStop},
		"wrong payload kind": {Kind: v1.AgentCommandVMDelete, VMApply: validApplySpec()},
		"multiple variants":  {Kind: v1.AgentCommandVMStop, VMApply: validApplySpec(), VMDelete: &VMDeleteCommandPayload{VMID: "vm_1"}},
		"incomplete apply":   {Kind: v1.AgentCommandVMApply, VMApply: &protocol.VMApplySpec{VMID: "vm_1"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := spec.marshalPayload(); err == nil {
				t.Fatal("invalid command spec was accepted")
			} else if !strings.Contains(err.Error(), ErrInvalidArgument.Error()) {
				t.Fatalf("error=%v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestCommandResultStatesAreKindSpecific(t *testing.T) {
	valid := []struct {
		kind  v1.AgentCommandKind
		state v1.AgentCommandResultState
	}{
		{v1.AgentCommandVMApply, v1.CommandResultRunning},
		{v1.AgentCommandVMStop, v1.CommandResultStopped},
		{v1.AgentCommandVMDelete, v1.CommandResultDeleted},
		{v1.AgentCommandSnapshotCreate, v1.CommandResultSnapshotReady},
		{v1.AgentCommandSnapshotRestore, v1.CommandResultRestored},
		{v1.AgentCommandVolumeResize, v1.CommandResultResized},
	}
	for _, item := range valid {
		if !resultStateAllowed(item.kind, true, item.state) {
			t.Errorf("kind %q rejected expected result state %q", item.kind, item.state)
		}
	}
	if !resultStateAllowed(v1.AgentCommandVMStop, false, v1.CommandResultFailed) {
		t.Fatal("failed result must use the failed state")
	}
	if resultStateAllowed(v1.AgentCommandVMStop, true, v1.CommandResultRunning) || resultStateAllowed(v1.AgentCommandKind("shell.exec"), false, v1.CommandResultFailed) {
		t.Fatal("unexpected command result state was accepted")
	}
}

func TestLeaseTokensAndIdempotencyFingerprintsAreStrongAndScoped(t *testing.T) {
	token, hash, err := newLeaseToken()
	if err != nil {
		t.Fatal(err)
	}
	if !validLeaseToken(token) || len(hash) != 64 || !isLowerHex(hash) {
		t.Fatalf("invalid generated lease token or hash: token length=%d hash=%q", len(token), hash)
	}
	if validLeaseToken("short") || validLeaseToken(strings.Repeat("!", 43)) {
		t.Fatal("malformed lease token accepted")
	}

	payload := []byte(`{"vm_id":"vm_1"}`)
	first, err := operationFingerprint("prj_1", "svc_1", "ins_1", "node_1", v1.AgentCommandVMStop, payload)
	if err != nil {
		t.Fatal(err)
	}
	identical, _ := operationFingerprint("prj_1", "svc_1", "ins_1", "node_1", v1.AgentCommandVMStop, payload)
	otherScope, _ := operationFingerprint("prj_2", "svc_1", "ins_1", "node_1", v1.AgentCommandVMStop, payload)
	otherPayload, _ := operationFingerprint("prj_1", "svc_1", "ins_1", "node_1", v1.AgentCommandVMStop, []byte(`{"vm_id":"vm_2"}`))
	if first != identical || first == otherScope || first == otherPayload {
		t.Fatal("operation request fingerprint is unstable or omits scope/payload")
	}
}
