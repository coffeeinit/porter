package migrate

import (
	"strings"
	"testing"
)

// T11 hermetic migrate.Plan phase-machine tests. Pure unit tests: no PG,
// no mover, no I/O.

func TestT11PlanEmptyTargetFailsValidate(t *testing.T) {
	cases := []struct {
		name string
		plan Plan
	}{
		{"empty target", Plan{VMID: "vm1", SourceNode: "n1", TargetNode: ""}},
		{"empty source", Plan{VMID: "vm1", SourceNode: "", TargetNode: "n2"}},
		{"empty vm", Plan{VMID: "", SourceNode: "n1", TargetNode: "n2"}},
		{"all empty", Plan{}},
		{"same node", Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.plan.Validate(); err == nil {
				t.Fatalf("plan %+v must fail validation", tc.plan)
			}
		})
	}
	if err := (Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2"}).Validate(); err != nil {
		t.Fatalf("distinct vm/source/target must validate: %v", err)
	}
}

func TestT11PlanFullAdvanceLockToDone(t *testing.T) {
	p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2"}
	want := []string{PhaseLock, PhaseStop, PhaseSnapshot, PhaseCopy, PhaseVerify, PhaseStart, PhaseDone}
	for i, phase := range want {
		if err := p.Advance(); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		if p.Phase != phase {
			t.Fatalf("step %d: want phase %q, got %q", i, phase, p.Phase)
		}
	}
	if p.Phase != PhaseDone {
		t.Fatalf("must end at done, got %q", p.Phase)
	}
	if err := p.Advance(); err == nil {
		t.Fatal("terminal done phase must not advance")
	} else if !strings.Contains(err.Error(), "unknown or terminal") {
		t.Fatalf("terminal error must name the phase problem, got: %v", err)
	}
}

func TestT11PlanAdvanceUnknownPhaseFails(t *testing.T) {
	for _, phase := range []string{"bogus", "DONE", "lock ", "verify-done"} {
		p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2", Phase: phase}
		if err := p.Advance(); err == nil {
			t.Fatalf("phase %q must fail", phase)
		}
	}
}

func TestT11PlanDigestVerifyMatrix(t *testing.T) {
	payload := []byte("t11-snapshot-payload")
	p := Plan{VMID: "vm1", SourceNode: "n1", TargetNode: "n2", Digest: DigestOf(payload)}
	if !p.Verified(payload) {
		t.Fatal("matching payload must verify true")
	}
	if p.Verified([]byte("t11-tampered-payload")) {
		t.Fatal("mismatched digest must verify false")
	}
	// Fail closed: empty plan digest never verifies, not even for empty input.
	if (Plan{}).Verified(payload) {
		t.Fatal("empty digest must fail closed on non-empty payload")
	}
	if (Plan{}).Verified(nil) {
		t.Fatal("empty digest must fail closed on empty payload")
	}
	if (Plan{Digest: ""}).Verified([]byte{}) {
		t.Fatal("empty digest must fail closed on empty slice")
	}
}
