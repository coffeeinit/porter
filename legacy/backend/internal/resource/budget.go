// Disruption budgets (SRS 58): one workload's tolerance for concurrent
// unavailability during maintenance. Pure policy — at most one bound may be
// set, and the eviction decision is a pure function of persisted pool state
// so the eviction gate, autoscaler and future drain paths share one rule.
// Library only; persistence lives with the callers.
package resource

import (
	"errors"
	"fmt"
)

// DisruptionBudget caps how many replicas of one workload may be evicted
// (migrated, drained, restarted for maintenance) concurrently. Set at most
// one bound: MinAvailable (keep at least N replicas serving) or
// MaxUnavailable (take at most N replicas out of service at once). The zero
// value configures no budget — every eviction is allowed.
//
// SRS 58: "10 replicas, minimum available = 8 → maintenance may evict at
// most 2."
type DisruptionBudget struct {
	MinAvailable   int `json:"min_available,omitempty"`
	MaxUnavailable int `json:"max_unavailable,omitempty"`
}

// Set reports whether any bound is configured. An unset budget disables the
// eviction gate entirely.
func (b DisruptionBudget) Set() bool {
	return b.MinAvailable != 0 || b.MaxUnavailable != 0
}

// Validate enforces the shape contract: non-negative bounds, at most one
// bound set. The zero budget (nothing set) is valid and means "no gate".
func (b DisruptionBudget) Validate() error {
	if b.MinAvailable < 0 || b.MaxUnavailable < 0 {
		return fmt.Errorf("disruption budget: bounds must be non-negative (min_available %d, max_unavailable %d)", b.MinAvailable, b.MaxUnavailable)
	}
	if b.MinAvailable > 0 && b.MaxUnavailable > 0 {
		return errors.New("disruption budget: set at most one of min_available, max_unavailable")
	}
	return nil
}

// CanEvict decides one eviction against persisted pool state (SRS 58).
// healthy counts the replicas currently serving, desired the configured pool
// size, alreadyEvicting the evictions in flight (each is assumed to have
// removed its replica from service). Evicting one more replica is allowed
// while availability stays within the configured bound; otherwise it returns
// allowed=false with an explicit reason the caller must surface.
func (b DisruptionBudget) CanEvict(healthy, desired, alreadyEvicting int) (bool, string) {
	if !b.Set() {
		return true, ""
	}
	if err := b.Validate(); err != nil {
		return false, err.Error()
	}
	if healthy < 0 || desired < 0 || alreadyEvicting < 0 {
		return false, fmt.Sprintf("disruption budget: negative pool state (healthy %d, desired %d, evicting %d)", healthy, desired, alreadyEvicting)
	}
	if desired == 0 {
		return false, "disruption budget: workload has no replicas"
	}
	if healthy > desired {
		return false, fmt.Sprintf("disruption budget: inconsistent pool state (healthy %d > desired %d)", healthy, desired)
	}
	switch {
	case b.MinAvailable > 0:
		// Availability after this eviction: healthy - alreadyEvicting - 1
		// (the in-flight evictions have already taken their replicas out).
		if healthy-alreadyEvicting-1 < b.MinAvailable {
			return false, fmt.Sprintf("disruption budget: min_available %d would be violated (healthy %d, evicting %d)", b.MinAvailable, healthy, alreadyEvicting)
		}
	case b.MaxUnavailable > 0:
		if alreadyEvicting+1 > b.MaxUnavailable {
			return false, fmt.Sprintf("disruption budget: max_unavailable %d would be exceeded (%d evictions already in flight)", b.MaxUnavailable, alreadyEvicting)
		}
	}
	return true, ""
}
