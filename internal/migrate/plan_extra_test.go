package migrate

import (
	"strings"
	"testing"
)

func TestPlanValidateRequiresIdentity(t *testing.T) {
	for _, p := range []Plan{
		{SourceNode: "n1", TargetNode: "n2"},
		{VMID: "vm1", TargetNode: "n2"},
		{VMID: "vm1", SourceNode: "n1"},
		{},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("plan %+v must fail validation", p)
		}
	}
}

func TestPlanAdvanceUnknownPhase(t *testing.T) {
	p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2", Phase: "bogus"}
	if err := p.Advance(); err == nil {
		t.Fatal("unknown phase must fail")
	} else if !strings.Contains(err.Error(), "unknown or terminal") {
		t.Fatalf("error wrong: %v", err)
	}
}

func TestPlanAdvanceStartsFromEmpty(t *testing.T) {
	p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2"}
	if err := p.Advance(); err != nil {
		t.Fatal(err)
	}
	if p.Phase != PhaseLock {
		t.Fatalf("first phase must be lock, got %q", p.Phase)
	}
}

func TestDigestOfShape(t *testing.T) {
	d1 := DigestOf([]byte("snap"))
	d2 := DigestOf([]byte("snap"))
	if d1 != d2 {
		t.Fatal("digest must be deterministic")
	}
	if len(d1) != 64 {
		t.Fatalf("hex sha256 must be 64 chars, got %q", d1)
	}
	if DigestOf([]byte("other")) == d1 {
		t.Fatal("different payload must differ")
	}
}
