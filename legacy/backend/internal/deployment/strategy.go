// Rollout strategies, rollback records and ephemeral jobs (SRS §23/24,
// PVE-09). Reconciliation executes; this package defines the strategy
// vocabulary, the rollback step order, and the one-shot job spec shared by
// `porter run` and AI sandbox jobs.
package deployment

import (
	"fmt"
	"strings"
)

// Strategies (SRS §23).
const (
	StrategyRecreate  = "recreate"
	StrategyRolling   = "rolling"
	StrategyBlueGreen = "blue-green"
	StrategyCanary    = "canary"
)

// ValidateStrategy gates the service spec.
func ValidateStrategy(s string) error {
	switch s {
	case StrategyRecreate, StrategyRolling, StrategyBlueGreen, StrategyCanary:
		return nil
	default:
		return fmt.Errorf("deployment: unknown strategy %q", s)
	}
}

// RollbackSteps is the SRS §24 rollback order: identify → resolve →
// provision → health → traffic → record. Persistent state survives.
var RollbackSteps = []string{
	"identify_version",
	"resolve_artifact",
	"provision_workload",
	"validate_health",
	"shift_traffic",
	"record_operation",
}

// Rollback is one rollback operation record.
type Rollback struct {
	DeploymentID  string
	TargetVersion string
	StepsDone     map[string]bool
}

// NewRollback starts a rollback record.
func NewRollback(deploymentID, targetVersion string) (*Rollback, error) {
	if deploymentID == "" || targetVersion == "" {
		return nil, fmt.Errorf("deployment: rollback needs a deployment and a target version")
	}
	return &Rollback{DeploymentID: deploymentID, TargetVersion: targetVersion, StepsDone: map[string]bool{}}, nil
}

// Complete marks a step; order is enforced.
func (r *Rollback) Complete(step string) error {
	want := ""
	for _, s := range RollbackSteps {
		if !r.StepsDone[s] {
			want = s
			break
		}
	}
	if want == "" {
		return fmt.Errorf("deployment: rollback already complete")
	}
	if step != want {
		return fmt.Errorf("deployment: want step %q next, got %q", want, step)
	}
	r.StepsDone[step] = true
	return nil
}

// Done reports whether all steps completed (audit preserved either way).
func (r *Rollback) Done() bool {
	for _, s := range RollbackSteps {
		if !r.StepsDone[s] {
			return false
		}
	}
	return true
}

// EphemeralJob is a run → exec → destroy one-shot (PVE-09 pve-microvm-run):
// find capacity, boot from template/snapshot, run the command, parse the
// exit code, destroy. Exit codes stay in the 0..255 range.
type EphemeralJob struct {
	Image     string
	Command   []string
	MemoryMiB int
	Cores     int
	NoNet     bool
}

// Validate gates job submission.
func (j EphemeralJob) Validate() error {
	if strings.TrimSpace(j.Image) == "" {
		return fmt.Errorf("deployment: ephemeral job needs an image")
	}
	if len(j.Command) == 0 {
		return fmt.Errorf("deployment: ephemeral job needs a command")
	}
	if j.MemoryMiB < 0 || j.Cores < 0 {
		return fmt.Errorf("deployment: negative resources")
	}
	return nil
}
