package policy

import "testing"

// Hermetic table over Merge: most-specific set field wins (policies are
// ordered specific -> general), unset fields inherit, empty input is Defaults.
func TestA5MergeTable(t *testing.T) {
	d := Defaults()
	cases := []struct {
		name   string
		in     []CapacityPolicy
		verify func(CapacityPolicy) bool
	}{
		{
			name: "empty is defaults",
			in:   nil,
			verify: func(got CapacityPolicy) bool {
				return *got.CPUOvercommit == 2.0 && *got.MemOvercommit == 1.0 &&
					*got.ReserveVCPU == 1 && *got.ReserveMemMiB == 2048 && *got.Enabled
			},
		},
		{
			name: "host cpu wins over global",
			in:   []CapacityPolicy{{Scope: ScopeHost, CPUOvercommit: fptr(1.0)}, d},
			verify: func(got CapacityPolicy) bool {
				return *got.CPUOvercommit == 1.0 && *got.MemOvercommit == 1.0 &&
					*got.ReserveVCPU == 1 && *got.Enabled
			},
		},
		{
			name: "general fills gaps",
			in:   []CapacityPolicy{{Scope: ScopePool, MemOvercommit: fptr(1.5)}},
			verify: func(got CapacityPolicy) bool {
				return *got.CPUOvercommit == 2.0 && *got.MemOvercommit == 1.5
			},
		},
		{
			name: "per-field most-specific wins",
			in: []CapacityPolicy{
				{Scope: ScopeHost, CPUOvercommit: fptr(1.0)},
				{Scope: ScopePool, CPUOvercommit: fptr(3.0), MemOvercommit: fptr(1.5)},
				{Scope: ScopeProvider, ReserveVCPU: iptr(4)},
			},
			verify: func(got CapacityPolicy) bool {
				return *got.CPUOvercommit == 1.0 && *got.MemOvercommit == 1.5 &&
					*got.ReserveVCPU == 4 && *got.ReserveMemMiB == 2048
			},
		},
		{
			name: "explicit disable wins",
			in:   []CapacityPolicy{{Scope: ScopeHost, Enabled: bptr(false)}, d},
			verify: func(got CapacityPolicy) bool {
				return !*got.Enabled && *got.CPUOvercommit == 2.0
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.in...)
			if !tc.verify(got) {
				t.Fatalf("Merge mismatch: %+v", got)
			}
			if _, _, err := EffectiveCapacity(8, 32768, got); err != nil {
				t.Fatalf("merged policy must be usable: %v", err)
			}
		})
	}
}
