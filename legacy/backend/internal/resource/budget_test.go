package resource

import "testing"

// Table-driven CanEvict checks (SRS 58): 10 replicas, min available 8 → at
// most 2 evicting at once.
func TestDisruptionBudgetCanEvict(t *testing.T) {
	tests := []struct {
		name       string
		b          DisruptionBudget
		healthy    int
		desired    int
		evicting   int
		want       bool
		wantReason bool // reason must be non-empty exactly when !want
	}{
		{"no budget allows everything", DisruptionBudget{}, 0, 0, 0, true, false},
		{"no budget allows even empty pools", DisruptionBudget{}, 0, 0, 5, true, false},
		{"srs example first eviction", DisruptionBudget{MinAvailable: 8}, 10, 10, 0, true, false},
		{"srs example second eviction", DisruptionBudget{MinAvailable: 8}, 10, 10, 1, true, false},
		{"srs example third eviction blocked", DisruptionBudget{MinAvailable: 8}, 10, 10, 2, false, true},
		{"eviction below min available blocked", DisruptionBudget{MinAvailable: 8}, 7, 10, 0, false, true},
		{"min above desired blocks all", DisruptionBudget{MinAvailable: 11}, 10, 10, 0, false, true},
		{"min equal to desired allows nothing", DisruptionBudget{MinAvailable: 10}, 10, 10, 0, false, true},
		{"max unavailable first eviction", DisruptionBudget{MaxUnavailable: 2}, 10, 10, 0, true, false},
		{"max unavailable second eviction", DisruptionBudget{MaxUnavailable: 2}, 10, 10, 1, true, false},
		{"max unavailable exhausted", DisruptionBudget{MaxUnavailable: 2}, 10, 10, 2, false, true},
		{"desired zero blocks", DisruptionBudget{MinAvailable: 1}, 0, 0, 0, false, true},
		{"desired zero blocks max variant", DisruptionBudget{MaxUnavailable: 2}, 0, 0, 0, false, true},
		{"healthy above desired is inconsistent", DisruptionBudget{MinAvailable: 1}, 5, 3, 0, false, true},
		{"negative healthy blocks", DisruptionBudget{MinAvailable: 1}, -1, 3, 0, false, true},
		{"negative desired blocks", DisruptionBudget{MinAvailable: 1}, 3, -1, 0, false, true},
		{"negative evicting blocks", DisruptionBudget{MinAvailable: 1}, 3, 3, -1, false, true},
		{"both bounds set blocks", DisruptionBudget{MinAvailable: 1, MaxUnavailable: 1}, 3, 3, 0, false, true},
		{"negative bound blocks", DisruptionBudget{MinAvailable: -1}, 3, 3, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, reason := tt.b.CanEvict(tt.healthy, tt.desired, tt.evicting)
			if allowed != tt.want {
				t.Fatalf("CanEvict(%d,%d,%d) = %v (reason %q), want %v", tt.healthy, tt.desired, tt.evicting, allowed, reason, tt.want)
			}
			if tt.wantReason && reason == "" {
				t.Fatalf("blocked eviction must carry a reason, got empty")
			}
			if !tt.wantReason && reason != "" {
				t.Fatalf("allowed eviction must not carry a reason, got %q", reason)
			}
		})
	}
}

func TestDisruptionBudgetValidate(t *testing.T) {
	tests := []struct {
		name    string
		b       DisruptionBudget
		wantErr bool
	}{
		{"zero budget valid", DisruptionBudget{}, false},
		{"min only valid", DisruptionBudget{MinAvailable: 8}, false},
		{"max only valid", DisruptionBudget{MaxUnavailable: 2}, false},
		{"both set invalid", DisruptionBudget{MinAvailable: 8, MaxUnavailable: 2}, true},
		{"negative min invalid", DisruptionBudget{MinAvailable: -1}, true},
		{"negative max invalid", DisruptionBudget{MaxUnavailable: -2}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.b.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%+v) = %v, wantErr %v", tt.b, err, tt.wantErr)
			}
		})
	}
}

func TestDisruptionBudgetSet(t *testing.T) {
	if (DisruptionBudget{}).Set() {
		t.Fatal("zero budget must read as unset")
	}
	if !(DisruptionBudget{MinAvailable: 1}).Set() || !(DisruptionBudget{MaxUnavailable: 1}).Set() {
		t.Fatal("any bound set must read as set")
	}
}
