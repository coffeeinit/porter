package hostnet

import (
	"testing"
)

func TestParseLinkLine(t *testing.T) {
	iface, err := ParseLinkLine("2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "eth0" || !iface.Up || iface.MTU != 1500 {
		t.Fatalf("bad parse: %+v", iface)
	}
	if _, err := ParseLinkLine("garbage"); err == nil {
		t.Fatal("garbage must fail")
	}
}

func TestChangeValidate(t *testing.T) {
	if err := (Change{Op: "up", Dev: "eth0"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Change{Op: "addr-add", Dev: "eth0", Value: "10.0.0.5/24"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Change{Op: "addr-add", Dev: "", Value: "10.0.0.5/24"}).Validate(); err == nil {
		t.Fatal("missing device must fail")
	}
	if err := (Change{Op: "addr-add", Dev: "eth0", Value: "nope"}).Validate(); err == nil {
		t.Fatal("bad CIDR must fail")
	}
	if err := (Change{Op: "explode"}).Validate(); err == nil {
		t.Fatal("unknown op must fail")
	}
}
