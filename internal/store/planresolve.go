// Plan resolution for quota admission (SRS §46/§48): a project's effective
// plan is its newest active subscription's plan; without one, the org's own
// subscription (billing_subscriptions is project-keyed, so an org-level
// subscription stores the org ID in project_id); otherwise the "default"
// plan preserves legacy behavior. Limits come from plan_limits; a plan with
// no rows resolves to an empty map, which OverQuota treats as pass-open.
package store

import (
	"porter/internal/billing"
)

// resolvePlanID walks the subscription resolution order — project first,
// then org — returning the first active plan, or "default" when neither
// subject has one. subsFor fetches a subject's subscriptions, newest first.
// Pure function — no DB, safe in unit tests.
func resolvePlanID(projectID, orgID string, subsFor func(id string) []billing.Subscription) string {
	for _, id := range []string{projectID, orgID} {
		if id == "" {
			continue
		}
		if planID, ok := pickActiveSubscription(subsFor(id)); ok {
			return planID
		}
	}
	return "default"
}

// ResolvePlanForProject returns the effective plan ID and its plan_limits
// map for quota admission. An empty projectID (project not created yet, e.g.
// pre-create admission) resolves through the org; an empty orgID falls
// through to the default plan. A subscribed plan with zero limit rows
// resolves to an empty map — unlimited, never an inherited default.
func (s *Store) ResolvePlanForProject(projectID, orgID string) (string, map[string]int64) {
	planID := resolvePlanID(projectID, orgID, s.ListSubscriptions)
	return planID, s.PlanLimits(planID)
}
