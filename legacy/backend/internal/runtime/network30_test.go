package runtime

import (
	"strings"
	"testing"

	"porter/internal/netmgr"
)

func TestAllocate30Math(t *testing.T) {
	m := NewNetworkManager("", "")
	a, err := m.AllocateVMNetwork30("proj", 1, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.Subnet, "/30") || !strings.HasSuffix(a.IPAddress, ".6") || !strings.HasSuffix(a.Gateway, ".5") {
		t.Fatalf("bad /30 block: %+v", a)
	}
	b, err := netmgr.AllocateVMNetwork30(10, 1, "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.CIDR != "10.42.10.6/30" || b.GatewayAddr != "10.42.10.5" {
		t.Fatalf("bad netmgr /30: %+v", b)
	}
}
