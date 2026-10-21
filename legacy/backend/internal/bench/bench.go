// Staged boot benchmark recorder (PVE-09/10). The bench harness times
// start → serial-socket → shell → agent on reference hardware; this package
// records those stage transitions and summarizes runs for SLO dashboards.
// Timing executes in the harness; here is the shared record vocabulary.
package bench

import (
	"fmt"
	"sort"
	"time"
)

// Stages in boot order. Serial must precede shell, shell precedes agent.
const (
	StageStart  = "start"
	StageSerial = "serial"
	StageShell  = "shell"
	StageAgent  = "agent"
)

// order gates stage transitions.
var order = map[string]int{StageStart: 0, StageSerial: 1, StageShell: 2, StageAgent: 3}

// Run is one benchmark run: VM spec plus observed stage timestamps.
type Run struct {
	Name      string
	MemoryMiB int
	Kernel    string
	RootFS    string
	Started   time.Time
	Stages    map[string]time.Time
	RSSMiB    int64 // host QEMU/Firecracker RSS at shell stage
}

// NewRun starts a run clock.
func NewRun(name string, memMiB int) *Run {
	return &Run{Name: name, MemoryMiB: memMiB, Started: time.Now(), Stages: map[string]time.Time{}}
}

// Mark records a stage; stages must advance in boot order.
func (r *Run) Mark(stage string, at time.Time) error {
	want, ok := order[stage]
	if !ok {
		return fmt.Errorf("bench: unknown stage %q", stage)
	}
	// Start (order 0) is implicitly complete at run start, so the first
	// markable stage is serial and every stage follows its predecessor.
	max := 0
	for s := range r.Stages {
		if order[s] > max {
			max = order[s]
		}
	}
	if want != max+1 {
		return fmt.Errorf("bench: stage %q out of order", stage)
	}
	if at.Before(r.Started) {
		return fmt.Errorf("bench: stage %q predates start", stage)
	}
	r.Stages[stage] = at
	return nil
}

// Summary renders serial/shell/agent latencies from start.
func (r *Run) Summary() map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, s := range []string{StageSerial, StageShell, StageAgent} {
		if at, ok := r.Stages[s]; ok {
			out[s] = at.Sub(r.Started)
		}
	}
	return out
}

// P95 returns the 95th percentile of a sample set (SLO math).
func P95(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), samples...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := (len(cp)*95 + 99) / 100 // ceil(0.95*n)
	if idx < 1 {
		idx = 1
	}
	return cp[idx-1]
}
