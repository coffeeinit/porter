package store

import (
	"testing"
	"time"

	"porter/internal/billing"
)

func TestPickActiveSubscription(t *testing.T) {
	now := time.Now()
	subs := []billing.Subscription{
		{ID: "new-cancelled", ProjectID: "p", PlanID: "pro", State: "cancelled", StartedAt: now},
		{ID: "mid-active", ProjectID: "p", PlanID: "team", State: "active", StartedAt: now.Add(-time.Hour)},
		{ID: "old-active", ProjectID: "p", PlanID: "starter", State: "active", StartedAt: now.Add(-2 * time.Hour)},
	}
	planID, ok := pickActiveSubscription(subs)
	if !ok || planID != "team" {
		t.Fatalf("expected newest active plan %q, got %q ok=%v", "team", planID, ok)
	}
	// Empty state counts as active (legacy rows).
	if planID, ok := pickActiveSubscription([]billing.Subscription{{PlanID: "x"}}); !ok || planID != "x" {
		t.Fatalf("empty state should count as active, got %q ok=%v", planID, ok)
	}
	// No active subscription.
	if _, ok := pickActiveSubscription([]billing.Subscription{{PlanID: "x", State: "cancelled"}}); ok {
		t.Fatal("cancelled-only should report no active subscription")
	}
	if _, ok := pickActiveSubscription(nil); ok {
		t.Fatal("nil should report no active subscription")
	}
}

func TestOverQuota(t *testing.T) {
	// Missing key = unlimited (open).
	if _, over := OverQuota(map[string]int64{}, "max_vms", 1000); over {
		t.Fatal("missing key should pass open")
	}
	if _, over := OverQuota(nil, "max_projects", 1000); over {
		t.Fatal("nil limits should pass open")
	}
	// Within quota.
	if _, over := OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 3); over {
		t.Fatal("equal-to-limit should fit")
	}
	// Over quota returns the limit.
	lim, over := OverQuota(map[string]int64{"max_vms": 3}, "max_vms", 4)
	if !over || lim != 3 {
		t.Fatalf("expected over with limit 3, got limit=%d over=%v", lim, over)
	}
	// Other keys are independent.
	if _, over := OverQuota(map[string]int64{"max_vms": 3}, "max_mem_mib", 1<<20); over {
		t.Fatal("unrelated key should pass open")
	}
}

// TestActiveSubscriptionPG exercises the pool-backed method; requires a live DB.
func TestActiveSubscriptionPG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres quota test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	const projID = "quota-test-proj"
	if err := s.PutPlan(billing.Plan{ID: "quota-test-plan", Name: "Quota Test", MonthlyCents: 100}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if _, err := s.CreateSubscription(projID, "quota-test-plan"); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	planID, ok := s.ActiveSubscription(projID)
	if !ok || planID != "quota-test-plan" {
		t.Fatalf("expected active plan %q, got %q ok=%v", "quota-test-plan", planID, ok)
	}
	if _, ok := s.ActiveSubscription("quota-test-proj-none"); ok {
		t.Fatal("unknown project should have no active subscription")
	}
}
