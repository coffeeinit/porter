package storage

import "testing"

func TestCloneValidate(t *testing.T) {
	ok := ClonePlan{SourceVolume: "v1", TargetVolume: "v2", NewMAC: "m2", NewIP: "ip2", SourceMAC: "m1", SourceIP: "ip1"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	reuse := ok
	reuse.NewMAC = "m1"
	if err := reuse.Validate(); err == nil {
		t.Fatal("MAC reuse must fail")
	}
}

func TestResizeValidate(t *testing.T) {
	if err := (ResizeOp{Volume: "v", Kind: ResizeExpand, SizeMiB: 1024, Online: true}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ResizeOp{Volume: "v", Kind: ResizeShrink, SizeMiB: 512, Online: true}).Validate(); err == nil {
		t.Fatal("online shrink must fail")
	}
}
