package runtime

import "testing"

func TestVirtioMemValidate(t *testing.T) {
	pool := VirtioMemPool{BaseMiB: 256, MaxMiB: 2048}
	if err := (VirtioMemResize{RequestedMiB: 512, Pool: pool}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (VirtioMemResize{RequestedMiB: 512, BalloonActive: true, Pool: pool}).Validate(); err == nil {
		t.Fatal("balloon+virtio-mem must never mix")
	}
	if err := (VirtioMemResize{RequestedMiB: 128, Pool: pool}).Validate(); err == nil {
		t.Fatal("below-base must fail")
	}
	if err := (VirtioMemResize{RequestedMiB: 4096, Pool: pool}).Validate(); err == nil {
		t.Fatal("above-max must fail")
	}
}
