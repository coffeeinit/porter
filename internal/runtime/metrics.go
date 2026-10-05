package runtime

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"porter/internal/types"
)

// Collector tails per-VM Firecracker log files into the store rings and ships
// new bytes durably into vm_logs, and records a heartbeat metric per VM
// (manual §8: logger once, metrics 60s + Flush). File offsets are tracked
// per VM so each line ships exactly once; truncation (rotation) resets the
// offset. Best-effort: missing files (dev/Windows) simply yield nothing.
type Collector struct {
	store   Store
	logsDir string
	offsets map[string]int64
	ticks   int
}

// Store is the persistence surface the collector needs (rings for the hot
// tail, vm_logs + metrics_samples for durable history, retention pruners).
type Store interface {
	AppendLog(vmID, line string)
	AppendVMLog(vmID, projectID, line string) error
	AddMetric(m *types.MetricSample) error
	GetVM(id string) (*types.VM, bool)
	PruneVMLogs(olderThan time.Time) int64
	PruneMetrics(olderThan time.Time) int64
}

// NewCollector builds the log/metrics consumer for one logs dir.
func NewCollector(st Store, logsDir string) *Collector {
	return &Collector{store: st, logsDir: logsDir, offsets: map[string]int64{}}
}

// PollOnce ships new log bytes per VM (ring + durable vm_logs) and records
// one liveness metric per supplied VM id. Offsets make shipping exactly-once
// per file position; values are counts/strings only.
func (c *Collector) PollOnce(vmIDs []string) {
	if c == nil || c.store == nil {
		return
	}
	for _, id := range vmIDs {
		c.shipLogs(id)
		_ = c.store.AddMetric(&types.MetricSample{
			VMID: id, Metric: "porter.heartbeat",
			Value: 1, TS: time.Now(),
		})
	}
	c.ticks++
	if c.ticks%24 == 0 { // ~daily on a 60s interval: bound both tables
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		c.store.PruneVMLogs(cutoff)
		c.store.PruneMetrics(cutoff)
	}
}

// shipLogs appends only bytes past the tracked offset for one VM's log file
// to the ring and, durably, to vm_logs. Secret-looking lines are dropped
// before either sink. Truncation (log rotation) resets the offset.
func (c *Collector) shipLogs(id string) {
	path := filepath.Join(c.logsDir, id+".log")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	off := c.offsets[id]
	if st.Size() < off {
		off = 0 // rotated/truncated: start over, never re-ship old bytes twice silently
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	project := ""
	if vm, ok := c.store.GetVM(id); ok && vm != nil {
		project = vm.ProjectID
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var pos = off
	for sc.Scan() {
		line := sc.Bytes()
		pos += int64(len(line)) + 1 // + newline
		ln := strings.TrimSpace(string(line))
		if ln == "" || strings.Contains(ln, "API_KEY") || strings.Contains(ln, "SECRET") {
			continue
		}
		c.store.AppendLog(id, ln)
		_ = c.store.AppendVMLog(id, project, ln)
	}
	c.offsets[id] = pos
}

// Start polls every interval until ctx ends.
func (c *Collector) Start(ctx context.Context, interval time.Duration, list func() []string) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.PollOnce(list())
		}
	}
}
