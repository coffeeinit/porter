// Agent bootstrap ordering contract (PVE-19): the node agent install is
// idempotent (stamp-guarded reruns), ordered before the workloads that need
// it (Before= ordering), applied on configure + trigger, and never reverted
// automatically — a failed apply quarantines the node instead of rolling
// back to an unknown state.
package agent

import "fmt"

// Bootstrap states.
const (
	BootstrapPending = "pending"
	BootstrapApplied = "applied"
	BootstrapAlready = "already" // stamp present: idempotent no-op
	BootstrapFailed  = "failed"
)

// Bootstrap records one agent-install attempt on a node.
type Bootstrap struct {
	NodeID  string
	Version string
	State   string
	Stamp   bool // stamp file present from a previous apply
}

// Apply resolves the attempt: stamp present means already-done, otherwise
// the caller runs the privileged installer and reports back via Report.
func (b *Bootstrap) Apply() string {
	if b.Stamp {
		b.State = BootstrapAlready
		return BootstrapAlready
	}
	b.State = BootstrapPending
	return BootstrapPending
}

// Report records the installer outcome. Failed attempts quarantine the node
// (failed state) — never silent revert.
func (b *Bootstrap) Report(err error) string {
	if err != nil {
		b.State = BootstrapFailed
		return BootstrapFailed
	}
	b.State = BootstrapApplied
	b.Stamp = true
	return BootstrapApplied
}

// Validate gates bootstrap records.
func (b Bootstrap) Validate() error {
	if b.NodeID == "" || b.Version == "" {
		return fmt.Errorf("agent: bootstrap needs node id and version")
	}
	return nil
}
