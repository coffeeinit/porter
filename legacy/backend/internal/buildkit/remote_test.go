package buildkit

import (
	"context"
	"io"
	"strings"
	"testing"

	"porter/internal/netmgr"
	"porter/internal/types"
)

type stubProv struct{ execs int }

func (s *stubProv) NetSpec(vm *types.VM) (netmgr.BootSpec, error) {
	return netmgr.BootSpec{CIDR: "10.42.7.9/30", GatewayAddr: "10.42.7.8", MacAddress: "02:00:00:00:00:01", HostDevName: "tap0"}, nil
}

func (s *stubProv) Boot(vm *types.VM, spec netmgr.BootSpec) error { return nil }

func (s *stubProv) Exec(_ context.Context, _ string, _ []string, _ io.Reader, _ io.Writer) error {
	s.execs++
	return nil
}

func (s *stubProv) Stop(vm *types.VM) error { return nil }

func TestUseIsolatedVM(t *testing.T) {
	b := &Builder{}
	if err := b.UseIsolatedVM(context.Background(), &stubProv{}, "porter-builder", "/tmp/builder.ext4", 0, 0); err != nil {
		t.Fatal(err)
	}
	if b.Addr != "tcp://10.42.7.9:1234" {
		t.Fatalf("wrong daemon addr: %q", b.Addr)
	}
	if err := b.UseIsolatedVM(context.Background(), nil, "x", "y", 0, 0); err == nil {
		t.Fatal("nil provisioner must fail")
	}
	if err := b.UseIsolatedVM(context.Background(), &stubProv{}, "x", "", 0, 0); err == nil ||
		!strings.Contains(err.Error(), "staged") {
		t.Fatal("missing rootfs must fail explicitly")
	}
}
