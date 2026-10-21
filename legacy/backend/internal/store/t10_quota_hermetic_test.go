// T10 hermetic quota coverage: OverQuota semantics plus one PG-gated
// round-trip that skips without PORTER_TEST_DATABASE_URL (store_test.go
// pattern). Everything else here is pure — no DB, no network.
package store

import (
	"fmt"
	"testing"
	"time"

	"porter/internal/billing"
)

func TestT10OverQuotaSemantics(t *testing.T) {
	// Missing key = unlimited (quotas pass open, never a silent deny).
	if lim, over := OverQuota(map[string]int64{}, "max_vms", 1<<30); over || lim != 0 {
		t.Fatalf("missing key should pass open, got lim=%d over=%v", lim, over)
	}
	if _, over := OverQuota(nil, "max_projects", 1<<30); over {
		t.Fatal("nil limits should pass open")
	}
	// Unrelated keys are independent.
	if _, over := OverQuota(map[string]int64{"max_vms": 3}, "max_mem_mib", 1<<20); over {
		t.Fatal("unrelated key should pass open")
	}
	// Under quota fits.
	if _, over := OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 2); over {
		t.Fatal("under-limit usage should fit")
	}
	// Equal-to-limit fits: the argument is would-be usage, not current.
	for key, lim := range map[string]int64{"max_projects": 2, "max_vms": 3, "max_mem_mib": 512} {
		if _, over := OverQuota(map[string]int64{key: lim}, key, lim); over {
			t.Fatalf("%s: equal-to-limit should fit", key)
		}
	}
	// Over by one returns the limit so callers can name it in the 403.
	lim, over := OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 4)
	if !over || lim != 3 {
		t.Fatalf("expected over with limit 3, got lim=%d over=%v", lim, over)
	}
	lim, over = OverQuota(map[string]int64{"max_mem_mib": 512}, "max_mem_mib", 513)
	if !over || lim != 512 {
		t.Fatalf("expected mem over with limit 512, got lim=%d over=%v", lim, over)
	}
	// Zero usage always fits a non-negative limit.
	if _, over := OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 0); over {
		t.Fatal("zero usage should fit")
	}
}

// TestT10OverQuotaExplicitZero documents the real limit-zero behavior of
// OverQuota itself: an explicit 0 entry is a limit of zero (would-be 0 fits,
// would-be 1 is over) — it is NOT unlimited at this layer. The "0 means no
// opinion" convention lives one layer up, in the bootReplica mem clamp
// (api.go: `maxMem > 0` guard), where an absent or zero max_mem_mib skips
// clamping entirely.
func TestT10OverQuotaExplicitZero(t *testing.T) {
	lim, over := OverQuota(map[string]int64{"max_vms": 0}, "max_vms", 0)
	if over || lim != 0 {
		t.Fatalf("would-be 0 against limit 0 should fit, got lim=%d over=%v", lim, over)
	}
	lim, over = OverQuota(map[string]int64{"max_vms": 0}, "max_vms", 1)
	if !over || lim != 0 {
		t.Fatalf("would-be 1 against explicit limit 0 is over, got lim=%d over=%v", lim, over)
	}
}

// TestT10QuotaLimitsRoundTripPG is the only DB-backed test in this file and
// follows the store_test.go pattern: skip when no test database is set.
func TestT10QuotaLimitsRoundTripPG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres quota test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	planID := fmt.Sprintf("test-t10-limit-plan-%d", time.Now().UnixNano())
	if err := s.PutPlan(billing.Plan{ID: planID, Name: "T10 Limit Test"}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := s.SetPlanLimit(planID, "max_vms", 5); err != nil {
		t.Fatalf("set plan limit: %v", err)
	}
	lims := s.PlanLimits(planID)
	if lims["max_vms"] != 5 {
		t.Fatalf("max_vms = %v, want 5 (limits: %v)", lims["max_vms"], lims)
	}
	// The round-tripped map must drive OverQuota the same as a literal map.
	if _, over := OverQuota(lims, "max_vms", 5); over {
		t.Fatal("equal-to-limit should fit via round-tripped limits")
	}
	if lim, over := OverQuota(lims, "max_vms", 6); !over || lim != 5 {
		t.Fatalf("expected over with limit 5, got lim=%d over=%v", lim, over)
	}
}
