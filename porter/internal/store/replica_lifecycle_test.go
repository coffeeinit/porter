package store

import (
	"testing"

	"porter/protocol"
)

func TestBindReplicaApplySpecIdentity(t *testing.T) {
	tests := []struct {
		name      string
		spec      protocol.VMApplySpec
		wantVMID  string
		wantRepID string
		wantErr   bool
	}{
		{
			name:      "matching identities",
			spec:      protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-1"},
			wantVMID:  "vm-1",
			wantRepID: "replica-1",
		},
		{
			name:      "legacy empty replica identity is filled",
			spec:      protocol.VMApplySpec{VMID: "vm-1"},
			wantVMID:  "vm-1",
			wantRepID: "replica-1",
		},
		{
			name:    "mismatched VM identity",
			spec:    protocol.VMApplySpec{VMID: "vm-other", ReplicaID: "replica-1"},
			wantErr: true,
		},
		{
			name:    "mismatched replica identity",
			spec:    protocol.VMApplySpec{VMID: "vm-1", ReplicaID: "replica-other"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bindReplicaApplySpec(tc.spec, "vm-1", "replica-1")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected identity mismatch to be rejected")
				}
				return
			}
			if err != nil {
				t.Fatalf("bind apply spec: %v", err)
			}
			if got.VMID != tc.wantVMID || got.ReplicaID != tc.wantRepID {
				t.Fatalf("bound identity = VM %q replica %q, want VM %q replica %q", got.VMID, got.ReplicaID, tc.wantVMID, tc.wantRepID)
			}
		})
	}
}
