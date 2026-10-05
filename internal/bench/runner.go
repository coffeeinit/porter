// Benchmark results and SLO checks (PVE-09 remainder). The harness records
// stage timings via Run; results persist to bench_runs for dashboards and
// placement-weight tuning. Budgets are per-spec ceilings the P95 must hold.
package bench

import (
	"fmt"
	"time"
)

// Result is one persisted benchmark run summary.
type Result struct {
	Name      string
	MemoryMiB int
	Serial    time.Duration
	Shell     time.Duration
	Agent     time.Duration
	RSSMiB    int64
	At        time.Time
}

// FromRun converts a completed run (serial+shell required, agent optional)
// into a storable result.
func FromRun(r *Run) (Result, error) {
	sum := r.Summary()
	shell, ok := sum[StageShell]
	if !ok {
		return Result{}, fmt.Errorf("bench: run %q has no shell stage", r.Name)
	}
	return Result{
		Name: r.Name, MemoryMiB: r.MemoryMiB,
		Serial: sum[StageSerial], Shell: shell, Agent: sum[StageAgent],
		RSSMiB: r.RSSMiB, At: time.Now(),
	}, nil
}

// Budget is the SLO ceiling set per spec (e.g. standard guest shell < 10s
// on reference hardware; Atom floor numbers live in the features doc).
type Budget struct {
	MaxShell  time.Duration
	MaxAgent  time.Duration
	MaxRSSMiB int64
}

// Check verifies a result against its budget, naming the breach.
func (b Budget) Check(r Result) error {
	if r.Shell > b.MaxShell {
		return fmt.Errorf("bench: shell %v over budget %v", r.Shell, b.MaxShell)
	}
	if b.MaxAgent > 0 && r.Agent > b.MaxAgent {
		return fmt.Errorf("bench: agent %v over budget %v", r.Agent, b.MaxAgent)
	}
	if b.MaxRSSMiB > 0 && r.RSSMiB > b.MaxRSSMiB {
		return fmt.Errorf("bench: rss %dMiB over budget %dMiB", r.RSSMiB, b.MaxRSSMiB)
	}
	return nil
}
