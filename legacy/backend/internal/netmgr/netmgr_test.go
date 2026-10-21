package netmgr

import (
	"net"
	"testing"
)

func TestAllocateSubnetUniqueness(t *testing.T) {
	n := NewNetManager()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		sub, err := n.AllocateSubnet()
		if err != nil {
			t.Fatalf("AllocateSubnet: %v", err)
		}
		if seen[sub.String()] {
			t.Fatalf("duplicate subnet %s", sub.String())
		}
		seen[sub.String()] = true
	}
}

func TestAllocateSubnetExhaustion(t *testing.T) {
	n := NewNetManager()
	for i := 0; i < 256; i++ {
		if _, err := n.AllocateSubnet(); err != nil {
			t.Fatalf("unexpected error on iteration %d: %v", i, err)
		}
	}
	if _, err := n.AllocateSubnet(); err == nil {
		t.Fatal("expected error after exhausting 10.42.0.0/16")
	}
}

func TestAllocateIPRange(t *testing.T) {
	n := NewNetManager()
	sub, err := n.AllocateSubnet()
	if err != nil {
		t.Fatal(err)
	}
	ip, err := n.AllocateIP(sub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.42.0.10" {
		t.Fatalf("expected .10, got %s", ip.String())
	}
	ip2, _ := n.AllocateIP(sub, 5)
	if ip2.String() != "10.42.0.15" {
		t.Fatalf("expected .15, got %s", ip2.String())
	}
	if _, err := n.AllocateIP(sub, 246); err == nil {
		t.Fatal("expected error for out-of-range replica index")
	}
}

func TestDeterministicMACStableAndDistinct(t *testing.T) {
	a, err := DeterministicMAC("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DeterministicMAC("vm-1")
	if a.String() != b.String() {
		t.Fatal("MAC must be stable per VM id")
	}
	c, _ := DeterministicMAC("vm-2")
	if a.String() == c.String() {
		t.Fatal("different VMs should get different MACs")
	}
}

func TestGatewayIP(t *testing.T) {
	sub := mustCIDR(t, "10.42.7.0/24")
	if got := GatewayIP(sub).String(); got != "10.42.7.1" {
		t.Fatalf("expected gateway .1, got %s", got)
	}
}

func mustCIDR(t *testing.T, cidr string) net.IPNet {
	t.Helper()
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse cidr %s: %v", cidr, err)
	}
	return *ipnet
}
