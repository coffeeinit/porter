package store

import (
	"fmt"
	"testing"
	"time"

	"porter/internal/billing"
)

// pgStore skips when no test database is configured.
func pgStore(t *testing.T) *Store {
	t.Helper()
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres billing test")
	}
	s := NewStore(testDSN())
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBillingPlansPG(t *testing.T) {
	s := pgStore(t)
	planID := fmt.Sprintf("test-plan-%d", time.Now().UnixNano())
	if err := s.PutPlan(billing.Plan{ID: planID, Name: "Test", MonthlyCents: 999}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := s.PutPrice(planID, "vcpu_seconds", 0.01, "seconds"); err != nil {
		t.Fatalf("put price: %v", err)
	}
	found := false
	for _, p := range s.ListPlans() {
		if p.ID != planID {
			continue
		}
		found = true
		if p.MonthlyCents != 999 {
			t.Fatalf("monthly mismatch: %+v", p)
		}
		if got := p.Prices["vcpu_seconds"]; got.UnitCents != 0.01 || got.Unit != "seconds" {
			t.Fatalf("price mismatch: %+v", p.Prices)
		}
	}
	if !found {
		t.Fatalf("plan %q not in ListPlans", planID)
	}
}

func TestBillingSubscriptionsPG(t *testing.T) {
	s := pgStore(t)
	planID := fmt.Sprintf("test-sub-plan-%d", time.Now().UnixNano())
	projID := fmt.Sprintf("test-sub-proj-%d", time.Now().UnixNano())
	if err := s.PutPlan(billing.Plan{ID: planID, Name: "Sub Test"}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	id, err := s.CreateSubscription(projID, planID)
	if err != nil || id == "" {
		t.Fatalf("create subscription: id=%q err=%v", id, err)
	}
	subs := s.ListSubscriptions(projID)
	if len(subs) == 0 {
		t.Fatal("expected at least one subscription")
	}
	if subs[0].PlanID != planID {
		t.Fatalf("newest subscription plan = %q, want %q", subs[0].PlanID, planID)
	}
	if got, ok := s.ActiveSubscription(projID); !ok || got != planID {
		t.Fatalf("active subscription = %q ok=%v, want %q", got, ok, planID)
	}
	if _, ok := s.ActiveSubscription(projID + "-missing"); ok {
		t.Fatal("unknown project must have no active subscription")
	}
}

func TestBillingMeterTotalsPG(t *testing.T) {
	s := pgStore(t)
	projID := fmt.Sprintf("test-meter-proj-%d", time.Now().UnixNano())
	since := time.Now().Add(-time.Hour)
	s.RecordUsage(projID, "vm-1", "vcpu_seconds", 100, "seconds", "bill-test-k1-"+projID)
	s.RecordUsage(projID, "vm-2", "vcpu_seconds", 50, "seconds", "bill-test-k2-"+projID)
	s.RecordUsage(projID, "vm-1", "network_bytes", 7, "bytes", "bill-test-k3-"+projID)
	got := s.MeterTotals(projID, since)
	if got["vcpu_seconds"] != 150 {
		t.Fatalf("vcpu_seconds = %v, want 150 (totals: %v)", got["vcpu_seconds"], got)
	}
	if got["network_bytes"] != 7 {
		t.Fatalf("network_bytes = %v, want 7 (totals: %v)", got["network_bytes"], got)
	}
	if empty := s.MeterTotals(projID+"-missing", since); len(empty) != 0 {
		t.Fatalf("unknown project should total empty, got %v", empty)
	}
}

func TestPlanLimitsPG(t *testing.T) {
	s := pgStore(t)
	planID := fmt.Sprintf("test-limit-plan-%d", time.Now().UnixNano())
	if err := s.PutPlan(billing.Plan{ID: planID, Name: "Limit Test"}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := s.SetPlanLimit(planID, "max_vms", 3); err != nil {
		t.Fatalf("set plan limit: %v", err)
	}
	lims := s.PlanLimits(planID)
	if lims["max_vms"] != 3 {
		t.Fatalf("max_vms = %v, want 3 (limits: %v)", lims["max_vms"], lims)
	}
}
