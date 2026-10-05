package runtime

import (
	"strings"
	"testing"

	"porter/internal/netmgr"
)

// TestAllocateVMNetworkUnifiedMath pins the T6b unification: the runtime
// allocator shares netmgr's base (10.42), gateway (.1), guest offset (.10+i)
// and MAC function, so both paths derive the same identity for a VM.
func TestAllocateVMNetworkUnifiedMath(t *testing.T) {
	m := NewNetworkManager("", "")
	spec, err := m.AllocateVMNetwork("proj-1", 0, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.IPAddress, "10.42.") {
		t.Fatalf("base must be 10.42, got %s", spec.IPAddress)
	}
	if !strings.HasSuffix(spec.IPAddress, ".10") {
		t.Fatalf("replica 0 must land on .10, got %s", spec.IPAddress)
	}
	if !strings.HasSuffix(spec.Gateway, ".1") {
		t.Fatalf("gateway must be .1, got %s", spec.Gateway)
	}
	wantMAC, _ := netmgr.DeterministicMAC("vm-1")
	if spec.MAC != wantMAC.String() {
		t.Fatalf("MAC must match netmgr.DeterministicMAC: got %s want %s", spec.MAC, wantMAC)
	}
	// Determinism: same inputs, same outputs.
	again, err := m.AllocateVMNetwork("proj-1", 0, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if *spec != *again {
		t.Fatalf("allocation must be deterministic: %+v vs %+v", spec, again)
	}
	// Bounds parity with netmgr (0..245).
	if _, err := m.AllocateVMNetwork("proj-1", 246, "vm-1"); err == nil {
		t.Fatal("replica index 246 must be rejected like netmgr")
	}
}
