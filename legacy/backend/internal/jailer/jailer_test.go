package jailer

import (
	"reflect"
	"testing"
)

func TestArgs(t *testing.T) {
	got, err := (Config{Binary: "/usr/bin/jailer", VMID: "vm-1", Firecracker: "/usr/bin/firecracker", SocketPath: "/run/porter/vm-1.sock"}).Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--id", "vm-1", "--exec-file", "/usr/bin/firecracker", "--api-sock", "/run/porter/vm-1.sock", "--cgroup-version", "v2", "--", "--api-sock", "/run/porter/vm-1.sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestArgsRejectsUnsafeInputs(t *testing.T) {
	cases := []Config{
		{Binary: "jailer", VMID: "../escape", Firecracker: "firecracker", SocketPath: "/run/vm.sock"},
		{Binary: "jailer", VMID: "vm", Firecracker: "firecracker", SocketPath: "relative.sock"},
		{Binary: "jailer", VMID: "vm", Firecracker: "firecracker", SocketPath: "/run/vm.sock", CgroupVersion: "v3"},
	}
	for _, tc := range cases {
		if _, err := tc.Args(); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", tc)
		}
	}
}
