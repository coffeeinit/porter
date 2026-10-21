// Quota enforcement over plan_limits (migration 0034): plans carry limits
// (keys: max_projects, max_vms, max_mem_mib) and subscriptions bind projects
// to plans. Admission checks resolve the project's active subscription plan
// first, the "default" plan as fallback; when no limits exist anywhere,
// quotas pass open — never a silent deny.
package store

import (
	"porter/internal/billing"
)

// pickActiveSubscription returns the plan ID of the newest active
// subscription. Callers pass newest-first rows (ListSubscriptions order).
// An empty state counts as active (pre-constraint legacy rows).
// Pure function — no DB, safe in unit tests.
func pickActiveSubscription(subs []billing.Subscription) (string, bool) {
	for _, sub := range subs {
		if sub.State == "" || sub.State == "active" {
			return sub.PlanID, true
		}
	}
	return "", false
}

// ActiveSubscription returns the plan ID of the project's newest active
// subscription, or ok=false when the project has none.
func (s *Store) ActiveSubscription(projectID string) (string, bool) {
	return pickActiveSubscription(s.ListSubscriptions(projectID))
}

// OverQuota reports whether a would-be usage value exceeds the plan limit
// for key. A missing key means unlimited (quotas pass open when no
// plan/limits exist). Pure function — no DB, safe in unit tests.
func OverQuota(limits map[string]int64, key string, wouldBe int64) (limit int64, over bool) {
	lim, ok := limits[key]
	if !ok {
		return 0, false
	}
	return lim, wouldBe > lim
}
