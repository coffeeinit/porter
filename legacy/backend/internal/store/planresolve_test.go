// Plan resolution tests: hermetic ordering checks over a stub subscription
// fetcher, plus one PG-gated round-trip that skips without
// PORTER_TEST_DATABASE_URL (store_test.go pattern).
package store

import (
	"fmt"
	"testing"
	"time"

	"porter/internal/billing"
)

func TestWP2ResolvePlanIDOrder(t *testing.T) {
	active := func(subject, plan string) []billing.Subscription {
		return []billing.Subscription{{ProjectID: subject, PlanID: plan, State: "active"}}
	}
	subs := map[string][]billing.Subscription{
		"p1":   active("p1", "pro"),
		"org1": active("org1", "team"),
	}
	fetch := func(id string) []billing.Subscription { return subs[id] }

	// Project subscription wins over the org subscription.
	if got := resolvePlanID("p1", "org1", fetch); got != "pro" {
		t.Errorf("project subscription must win: got plan %q, want pro", got)
	}
	// Org subscription applies when the project has none (pre-create).
	if got := resolvePlanID("", "org1", fetch); got != "team" {
		t.Errorf("org subscription must apply without a project: got %q, want team", got)
	}
	// A project with no subscription falls through to the org's plan.
	if got := resolvePlanID("p2", "org1", fetch); got != "team" {
		t.Errorf("org must back a subscription-less project: got %q, want team", got)
	}
	// Neither subject subscribed → the default plan.
	if got := resolvePlanID("p2", "org2", fetch); got != "default" {
		t.Errorf("no subscriptions must fall back to default: got %q", got)
	}
	// Empty IDs are skipped, never queried.
	queried := ""
	if got := resolvePlanID("", "", func(id string) []billing.Subscription {
		queried = id
		return nil
	}); got != "default" || queried != "" {
		t.Errorf("empty ids must skip straight to default, got plan %q queried %q", got, queried)
	}
}

func TestWP2ResolvePlanIDInactiveFallsThrough(t *testing.T) {
	// pickActiveSubscription's rule holds through resolution: a cancelled
	// project subscription is skipped; an empty-state one counts as active.
	subs := map[string][]billing.Subscription{
		"p1": {{ProjectID: "p1", PlanID: "old", State: "cancelled"}},
		// org1 absent from the map → not subscribed.
	}
	fetch := func(id string) []billing.Subscription { return subs[id] }
	if got := resolvePlanID("p1", "org1", fetch); got != "default" {
		t.Errorf("cancelled subscription must fall through to default, got %q", got)
	}
	subs["p1"] = []billing.Subscription{{ProjectID: "p1", PlanID: "legacy", State: ""}}
	if got := resolvePlanID("p1", "org1", fetch); got != "legacy" {
		t.Errorf("empty-state subscription counts as active, got %q", got)
	}
}

// TestWP2ResolvePlanForProjectPG is the only DB-backed test in this file and
// follows the store_test.go pattern: skip when no test database is set.
func TestWP2ResolvePlanForProjectPG(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres plan resolve test")
	}
	s := NewStore(testDSN())
	defer s.Close()
	suffix := time.Now().UnixNano()
	projID := fmt.Sprintf("test-wp2-proj-%d", suffix)
	orgID := fmt.Sprintf("test-wp2-org-%d", suffix)

	// No subscriptions anywhere → default plan, whatever rows it has.
	planID, limits := s.ResolvePlanForProject(projID, orgID)
	if planID != "default" {
		t.Fatalf("unsubscribed project must resolve to default, got %q", planID)
	}
	if limits == nil {
		t.Fatal("default plan limits must be a non-nil map (possibly empty)")
	}

	// An org-level subscription (org ID stored in project_id) with limits.
	orgPlan := fmt.Sprintf("test-wp2-org-plan-%d", suffix)
	if err := s.PutPlan(billing.Plan{ID: orgPlan, Name: "WP2 Org Plan"}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := s.SetPlanLimit(orgPlan, "max_vms", 7); err != nil {
		t.Fatalf("set plan limit: %v", err)
	}
	if _, err := s.CreateSubscription(orgID, orgPlan); err != nil {
		t.Fatalf("create org subscription: %v", err)
	}
	planID, limits = s.ResolvePlanForProject("", orgID)
	if planID != orgPlan || limits["max_vms"] != 7 {
		t.Fatalf("org subscription must win: got plan %q limits %v, want %q max_vms=7", planID, limits, orgPlan)
	}

	// A project subscription overrides the org's.
	projPlan := fmt.Sprintf("test-wp2-proj-plan-%d", suffix)
	if err := s.PutPlan(billing.Plan{ID: projPlan, Name: "WP2 Proj Plan"}); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	if err := s.SetPlanLimit(projPlan, "max_projects", 2); err != nil {
		t.Fatalf("set plan limit: %v", err)
	}
	if _, err := s.CreateSubscription(projID, projPlan); err != nil {
		t.Fatalf("create project subscription: %v", err)
	}
	planID, limits = s.ResolvePlanForProject(projID, orgID)
	if planID != projPlan || limits["max_projects"] != 2 {
		t.Fatalf("project subscription must win: got plan %q limits %v, want %q max_projects=2", planID, limits, projPlan)
	}
	// The resolved limits must drive OverQuota like any literal map.
	if lim, over := OverQuota(limits, "max_projects", 2); over || lim != 2 {
		t.Fatalf("equal-to-limit should fit via resolved limits, got lim=%d over=%v", lim, over)
	}
	if lim, over := OverQuota(limits, "max_projects", 3); !over || lim != 2 {
		t.Fatalf("expected over with limit 2, got lim=%d over=%v", lim, over)
	}
}
