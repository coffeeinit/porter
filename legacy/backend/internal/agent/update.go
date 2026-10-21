// Agent version-train rollout (FCM-15 + OCM-12 remainder): manual,
// per-host updates inside maintenance windows. The fleet never auto-updates;
// drift surfaces via heartbeat versions, and each host upgrades only inside
// its window after artifact verification.
package agent

import (
	"fmt"
	"time"
)

// Window is a weekly maintenance window (days in time.Weekday, hours UTC).
type Window struct {
	Days      []time.Weekday
	StartHour int // 0..23 inclusive
	EndHour   int // exclusive
}

// Validate gates window shape.
func (w Window) Validate() error {
	if len(w.Days) == 0 {
		return fmt.Errorf("agent: window needs at least one day")
	}
	if w.StartHour < 0 || w.StartHour > 23 || w.EndHour <= 0 || w.EndHour > 24 || w.EndHour <= w.StartHour {
		return fmt.Errorf("agent: bad window hours %d-%d", w.StartHour, w.EndHour)
	}
	return nil
}

// Allowed reports whether now falls inside the window.
func (w Window) Allowed(now time.Time) bool {
	if err := w.Validate(); err != nil {
		return false
	}
	t := now.UTC()
	for _, d := range w.Days {
		if d == t.Weekday() && t.Hour() >= w.StartHour && t.Hour() < w.EndHour {
			return true
		}
	}
	return false
}

// UpdatePlan is one approved per-host update decision.
type UpdatePlan struct {
	NodeID   string
	Current  string
	Desired  string
	Verified bool // artifact signature + checksum verified
	InWindow bool
}

// Decide validates an update may proceed: drift exists, artifact verified,
// window open. Denials name the reason (never a silent skip).
func Decide(p UpdatePlan) error {
	if !ShouldUpdate(p.Current, p.Desired) {
		return fmt.Errorf("agent: %s already at %s", p.NodeID, p.Desired)
	}
	if !p.Verified {
		return fmt.Errorf("agent: %s artifact %s unverified", p.NodeID, p.Desired)
	}
	if !p.InWindow {
		return fmt.Errorf("agent: %s outside maintenance window", p.NodeID)
	}
	return nil
}
