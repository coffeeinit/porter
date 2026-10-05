package policy

import "testing"

// NOTE: policy_test.go covers Evaluate first-deny-wins and the baseline
// EffectiveCapacity formula; merge_a5_test.go covers the Merge table. These
// T12 cases cover the remaining branches: overcommit floors, reserve clamp,
// empty Evaluate, and three-level per-field precedence.

func TestT12EffectiveCapacityEdges(t *testing.T) {
	if _, _, err := EffectiveCapacity(8, 32768, CapacityPolicy{
		Scope: ScopeGlobal, CPUOvercommit: fptr(0.5), MemOvercommit: fptr(1.0),
		ReserveVCPU: iptr(1), ReserveMemMiB: iptr(2048),
	}); err == nil {
		t.Fatal("cpu overcommit below 1.0 must fail")
	}
	if _, _, err := EffectiveCapacity(8, 32768, CapacityPolicy{
		Scope: ScopeGlobal, CPUOvercommit: fptr(2.0), MemOvercommit: fptr(0.9),
		ReserveVCPU: iptr(1), ReserveMemMiB: iptr(2048),
	}); err == nil {
		t.Fatal("mem overcommit below 1.0 must fail")
	}
	vcpu, mem, err := EffectiveCapacity(1, 1024, CapacityPolicy{
		Scope: ScopeGlobal, CPUOvercommit: fptr(2.0), MemOvercommit: fptr(1.0),
		ReserveVCPU: iptr(4), ReserveMemMiB: iptr(4096),
	})
	if err != nil {
		t.Fatal(err)
	}
	if vcpu != 0 || mem != 0 {
		t.Fatalf("reserve above phys must clamp to zero, got vcpu=%d mem=%d", vcpu, mem)
	}
	vcpu, mem, err = EffectiveCapacity(16, 65536, Merge(
		CapacityPolicy{Scope: ScopePool, MemOvercommit: fptr(1.5)},
		Defaults(),
	))
	if err != nil {
		t.Fatal(err)
	}
	if vcpu != 30 || mem != 95232 { // (16-1)*2.0, (65536-2048)*1.5
		t.Fatalf("merged pool policy wrong: vcpu=%d mem=%d", vcpu, mem)
	}
}

func TestT12EvaluateEmptyAllows(t *testing.T) {
	if d := Evaluate(nil); !d.Allow {
		t.Fatalf("empty stages must allow: %+v", d)
	}
}

func TestT12MergeThreeLevelPrecedence(t *testing.T) {
	got := Merge(
		CapacityPolicy{Scope: ScopeHost, CPUOvercommit: fptr(1.0)},
		CapacityPolicy{Scope: ScopePool, CPUOvercommit: fptr(3.0), MemOvercommit: fptr(1.5)},
		CapacityPolicy{Scope: ScopeProvider, CPUOvercommit: fptr(4.0), MemOvercommit: fptr(2.0), ReserveVCPU: iptr(2)},
		Defaults(),
	)
	if *got.CPUOvercommit != 1.0 {
		t.Fatalf("host cpu must win, got %v", *got.CPUOvercommit)
	}
	if *got.MemOvercommit != 1.5 {
		t.Fatalf("pool mem must win over provider, got %v", *got.MemOvercommit)
	}
	if *got.ReserveVCPU != 2 {
		t.Fatalf("provider reserve must fill host gap, got %v", *got.ReserveVCPU)
	}
	if *got.ReserveMemMiB != 2048 {
		t.Fatalf("global reserve mem must fill, got %v", *got.ReserveMemMiB)
	}
	if _, _, err := EffectiveCapacity(8, 32768, got); err != nil {
		t.Fatalf("three-level merged policy must be usable: %v", err)
	}
}
