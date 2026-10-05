package storage

import (
	"strings"
	"testing"
)

func TestShareValidate(t *testing.T) {
	ok := Share{Tag: "shared", HostPath: "/srv/ws", Driver: DriverVirtioFS}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Share{
		{Tag: "Bad Tag!", HostPath: "/srv", Driver: DriverVirtioFS},
		{Tag: "ok", HostPath: "relative", Driver: DriverVirtioFS},
		{Tag: "ok", HostPath: "/srv", Driver: "nfs"},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("must fail: %+v", bad)
		}
	}
}

func TestRendezvousAndMount(t *testing.T) {
	v := Share{Tag: "shared", HostPath: "/srv/ws", Driver: DriverVirtioFS}
	p, err := RendezvousPath("/var/run/porter", "vm/1", v)
	if err != nil || p != "/var/run/porter/vm-1-virtiofs.sock" {
		t.Fatalf("bad rendezvous %q %v", p, err)
	}
	n := Share{Tag: "data", HostPath: "/srv/d", Driver: Driver9P}
	p, err = RendezvousPath("/var/run/porter/", "vm1", n)
	if err != nil || p != "/var/run/porter/vm1-9p.conf" {
		t.Fatalf("bad rendezvous %q %v", p, err)
	}
	m, err := GuestMount(n, "/mnt/data")
	if err != nil || !strings.Contains(m, "trans=virtio") {
		t.Fatalf("bad mount %q %v", m, err)
	}
}
