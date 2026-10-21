// Auxiliary-VM subresource (OCM-16 remainder): paired helper VMs
// (headless chrome, builder, bastion) with independent lifecycles. Pairing
// is compensation-aware: firewall isolation + proxy routing must both land,
// or the pair rolls back. Unpair is agent-first, then DB.
package deployment

import "fmt"

// Auxiliary kinds.
const (
	AuxChrome  = "chrome"
	AuxBuilder = "builder"
	AuxBastion = "bastion"
)

// Pair states.
const (
	PairUnpaired  = "unpaired"
	PairPairing   = "pairing"
	PairPaired    = "paired"
	PairUnpairing = "unpairing"
)

// AuxLink binds one helper VM to its primary workload.
type AuxLink struct {
	WorkloadID string
	AuxID      string
	Kind       string
	State      string
	Firewall   bool // isolation rules applied
	Proxied    bool // control-plane proxy routing live
}

// Validate gates aux links: known kind, distinct ids.
func (l AuxLink) Validate() error {
	switch l.Kind {
	case AuxChrome, AuxBuilder, AuxBastion:
	default:
		return fmt.Errorf("deployment: unknown aux kind %q", l.Kind)
	}
	if l.WorkloadID == "" || l.AuxID == "" {
		return fmt.Errorf("deployment: aux link needs workload and aux ids")
	}
	if l.WorkloadID == l.AuxID {
		return fmt.Errorf("deployment: workload cannot pair with itself")
	}
	return nil
}

// Advance moves pairing forward; paired requires both compensations, and
// unpair completes only after the agent confirms teardown (caller passes
// agentDone=true once the helper VM is stopped).
func (l *AuxLink) Advance(agentDone bool) error {
	if err := l.Validate(); err != nil {
		return err
	}
	switch l.State {
	case "", PairUnpaired:
		l.State = PairPairing
	case PairPairing:
		if !l.Firewall || !l.Proxied {
			return fmt.Errorf("deployment: pair needs firewall + proxy before paired")
		}
		l.State = PairPaired
	case PairPaired:
		l.State = PairUnpairing
	case PairUnpairing:
		if !agentDone {
			return fmt.Errorf("deployment: unpair waits for agent teardown")
		}
		l.State = PairUnpaired
		l.Firewall, l.Proxied = false, false
	default:
		return fmt.Errorf("deployment: unknown pair state %q", l.State)
	}
	return nil
}
