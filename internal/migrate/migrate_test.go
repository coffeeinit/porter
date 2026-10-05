package migrate

import (
	"testing"
)

func TestPlanFlow(t *testing.T) {
	p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := p.Advance(); err != nil {
			t.Fatal(err)
		}
	}
	if p.Phase != PhaseDone {
		t.Fatalf("must end done, got %q", p.Phase)
	}
	if err := p.Advance(); err == nil {
		t.Fatal("terminal phase must stick")
	}
	if err := (Plan{VMID: "v", SourceNode: "n", TargetNode: "n"}).Validate(); err == nil {
		t.Fatal("same-node move must fail")
	}
}

func TestVerified(t *testing.T) {
	p := Plan{Digest: DigestOf([]byte("snap"))}
	if !p.Verified([]byte("snap")) {
		t.Fatal("matching payload must verify")
	}
	if p.Verified([]byte("other")) {
		t.Fatal("mismatch must not verify")
	}
	if (Plan{}).Verified([]byte("snap")) {
		t.Fatal("empty digest must fail closed")
	}
}
