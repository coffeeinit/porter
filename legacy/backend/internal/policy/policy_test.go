package policy

import "testing"

func TestEvaluate(t *testing.T) {
	if d := Evaluate([]Decision{Allow(StageAuthN), Allow(StageAuthZ)}); !d.Allow {
		t.Fatal("all-allow must pass")
	}
	d := Evaluate([]Decision{Allow(StageAuthN), Deny(StageQuota, "vcpu exhausted"), Allow(StagePolicy)})
	if d.Allow || d.Stage != StageQuota {
		t.Fatalf("first deny wins: %+v", d)
	}
}

func TestEffectiveCapacity(t *testing.T) {
	v, m, err := EffectiveCapacity(8, 32768, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if v != 14 || m != 30720 { // (8-1)*2.0, (32768-2048)*1.0
		t.Fatalf("got vcpu=%d mem=%d", v, m)
	}
	host := CapacityPolicy{Scope: ScopeHost, CPUOvercommit: fptr(1.0)}
	merged := Merge(host, Defaults())
	if *merged.CPUOvercommit != 1.0 || *merged.MemOvercommit != 1.0 {
		t.Fatalf("specific must win, general must fill: %+v", merged)
	}
	v2, _, err := EffectiveCapacity(8, 32768, merged)
	if err != nil || v2 != 7 {
		t.Fatalf("merged vcpu=%d err=%v", v2, err)
	}
	if _, _, err := EffectiveCapacity(8, 32768, CapacityPolicy{}); err == nil {
		t.Fatal("unset fields must fail")
	}
}
