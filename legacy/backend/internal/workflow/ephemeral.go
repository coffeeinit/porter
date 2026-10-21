// Ephemeral one-shot runs (PVE-08 remainder): the run → exec → destroy
// lifecycle for `porter run` and AI sandbox jobs. Execution stays in the
// agent (boot from template/snapshot, guest exec, destroy); this package
// owns the phase machine so every run is observable and never strands a VM.
package workflow

import "fmt"

// Ephemeral phases in lifecycle order.
const (
	EphemeralPrepared  = "prepared"
	EphemeralBooted    = "booted"
	EphemeralExecuted  = "executed"
	EphemeralDestroyed = "destroyed"
)

// EphemeralRun tracks one one-shot job against its durable task id.
type EphemeralRun struct {
	JobID string // tasks ledger id (kind=ephemeral)
	Image string
	Phase string
	Exit  *int // guest exit code once executed
}

// Advance moves the run forward exactly one phase. Destroyed is terminal;
// a run that fails mid-flight stays in-phase with its task FAILED, and the
// reaper destroys the VM out-of-band (never leak on error).
func (r *EphemeralRun) Advance(exit *int) error {
	switch r.Phase {
	case "", EphemeralPrepared:
		r.Phase = EphemeralBooted
	case EphemeralBooted:
		if exit == nil {
			return fmt.Errorf("workflow: booted -> executed needs an exit code")
		}
		r.Phase, r.Exit = EphemeralExecuted, exit
	case EphemeralExecuted:
		r.Phase = EphemeralDestroyed
	case EphemeralDestroyed:
		return fmt.Errorf("workflow: run already destroyed")
	default:
		return fmt.Errorf("workflow: unknown ephemeral phase %q", r.Phase)
	}
	return nil
}

// Destroyed reports terminal state.
func (r *EphemeralRun) Destroyed() bool { return r.Phase == EphemeralDestroyed }

// ParseExitCode validates a guest exit code (0-255, mirroring the
// pve-microvm-run JSON contract). Anything else fails closed: a malformed
// agent response must never become a success.
func ParseExitCode(v int) (int, error) {
	if v < 0 || v > 255 {
		return 0, fmt.Errorf("workflow: exit code %d outside 0-255", v)
	}
	return v, nil
}
