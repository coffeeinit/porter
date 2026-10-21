package runtime

import "testing"

func TestMemoryPolicy(t *testing.T) {
	ok := MemoryPolicy{SizeMiB: 512, BalloonMinMiB: 256}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := ok.AccountableMiB(); got != 512 {
		t.Fatalf("balloon adds no booking, got %d", got)
	}
	hot := MemoryPolicy{SizeMiB: 256, VirtioMemMiB: 768}
	if err := hot.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := hot.AccountableMiB(); got != 1024 {
		t.Fatalf("hotplug pool books, got %d", got)
	}
	for _, bad := range []MemoryPolicy{
		{SizeMiB: 32},
		{SizeMiB: 512, BalloonMinMiB: 128, VirtioMemMiB: 128},
		{SizeMiB: 256, BalloonMinMiB: 512},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("must fail: %+v", bad)
		}
	}
}
