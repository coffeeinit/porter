// Host cgroup sampler (OCM-22 execution): reads per-VM CPU + memory from
// the host cgroup filesystem. Paths follow the porter-vm-<id>.scope
// convention; anything absent (non-systemd hosts, dev machines) is an
// error and the pump skips the VM for that tick — never fabricated.
package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CgroupSampler reads <Base>/porter-<vmID>.scope/{cpu.stat,memory.current}.
type CgroupSampler struct {
	Base string // e.g. /sys/fs/cgroup/system.slice
}

// Sample reads one VM's usage. Missing files/dirs are errors (skip, loudly).
func (c CgroupSampler) Sample(vmID string) (Sample, error) {
	dir := filepath.Join(c.Base, "porter-"+sanitizeVMID(vmID)+".scope")
	memRaw, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return Sample{}, fmt.Errorf("metrics: cgroup %s: %w", vmID, err)
	}
	mem, err := strconv.ParseUint(strings.TrimSpace(string(memRaw)), 10, 64)
	if err != nil {
		return Sample{}, fmt.Errorf("metrics: cgroup %s memory: %w", vmID, err)
	}
	var micros float64
	if cpuRaw, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
		micros = cpuMicros(string(cpuRaw))
	}
	return Sample{
		VMID: vmID, At: time.Now(),
		Micros: micros, MemMiB: float64(mem) / (1024 * 1024),
	}, nil
}

// cpuMicros converts cgroup cpu.stat usage_usec to millicores over a
// nominal 1s window (best-effort rate; pump timestamps bound the error).
func cpuMicros(stat string) float64 {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "usage_usec" {
			if us, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				return float64(us) / 1000
			}
		}
	}
	return 0
}

// sanitizeVMID rejects path escapes in VM ids.
func sanitizeVMID(id string) string {
	if id == "" || strings.ContainsAny(id, "/\\. ") {
		return "invalid"
	}
	return id
}
