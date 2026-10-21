// Agent credential rotation + in-place upgrade intents (OCM-19): replace or
// patch a node's credentials and roll the agent binary forward/back without
// recreating the node. Execution stays behind the :9090 control bearer gate;
// this file owns the intent shapes so rotation is observable and auditable.
package agent

import "fmt"

// Rotation replaces node credentials without downtime: stage the new
// credential, verify it authenticates, then retire the old one.
type Rotation struct {
	NodeID      string
	Credential  string // new credential material (hashed at rest by the store)
	Replacement bool   // true = replace, false = additive patch
}

// Validate gates rotation intents.
func (r Rotation) Validate() error {
	if r.NodeID == "" || r.Credential == "" {
		return fmt.Errorf("agent: rotation needs node id and credential")
	}
	return nil
}

// Upgrade intents in lifecycle order.
const (
	UpgradeWanted   = "wanted"
	UpgradeStaged   = "staged"
	UpgradeActive   = "active"
	UpgradeRolledBk = "rolled-back"
)

// UpgradeIntent rolls one agent forward (or back) across versions.
type UpgradeIntent struct {
	NodeID      string
	FromVersion string
	ToVersion   string
	State       string
}

// Validate gates upgrade intents: versions must differ and be non-empty.
func (u UpgradeIntent) Validate() error {
	if u.NodeID == "" || u.FromVersion == "" || u.ToVersion == "" {
		return fmt.Errorf("agent: upgrade needs node and both versions")
	}
	if u.FromVersion == u.ToVersion {
		return fmt.Errorf("agent: upgrade versions must differ")
	}
	return nil
}

// Advance moves the intent forward one step; rolled-back is terminal.
func (u *UpgradeIntent) Advance() error {
	next := map[string]string{
		"":              UpgradeWanted,
		UpgradeWanted:   UpgradeStaged,
		UpgradeStaged:   UpgradeActive,
		UpgradeActive:   UpgradeRolledBk,
		UpgradeRolledBk: "",
	}[u.State]
	if next == "" {
		return fmt.Errorf("agent: unknown or terminal upgrade state %q", u.State)
	}
	u.State = next
	return nil
}
