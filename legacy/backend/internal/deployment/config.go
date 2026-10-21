// Explicit atomic config push (OCM-15 remainder): seed-minimal first boot,
// then every change ships as one validated push against a base revision.
// Stale base revisions are rejected so concurrent editors never silently
// overwrite each other; the push records a durable task for the applier.
package deployment

import "fmt"

// PushRequest is one atomic config change for a service.
type PushRequest struct {
	ServiceID string
	BaseRev   string // status.config_rev the editor saw
	Env       map[string]string
}

// ValidatePush gates a push: identity, non-empty change, and base-rev match
// against the applier's current revision (optimistic concurrency).
func ValidatePush(req PushRequest, currentRev string) error {
	if req.ServiceID == "" {
		return fmt.Errorf("deployment: push needs a service")
	}
	if len(req.Env) == 0 {
		return fmt.Errorf("deployment: push needs at least one variable")
	}
	for k := range req.Env {
		if k == "" {
			return fmt.Errorf("deployment: empty variable name")
		}
	}
	if req.BaseRev != currentRev {
		return fmt.Errorf("deployment: stale base rev %q (current %q): re-read and retry",
			req.BaseRev, currentRev)
	}
	return nil
}
