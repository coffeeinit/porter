// Host-side resource sampler (OCM-22): cgroup/CPU samples collected on the
// host feed usage and billing meters without any guest cooperation. Samples
// land in time-partitioned tables (vm_metrics_1s / vm_metrics_1m) so hot
// writes never contend with the control-plane tables.
package metrics

import (
	"fmt"
	"time"
)

// Sample is one host-side resource reading for a VM.
type Sample struct {
	VMID      string
	At        time.Time
	Micros    float64 // millicores used in the interval
	MemMiB    float64
	DiskBytes int64
	NetRx     int64
	NetTx     int64
}

// Validate gates samples: identity, timestamp, and non-negative readings.
func (s Sample) Validate() error {
	if s.VMID == "" {
		return fmt.Errorf("metrics: sample needs a vm id")
	}
	if s.At.IsZero() {
		return fmt.Errorf("metrics: sample needs a timestamp")
	}
	if s.Micros < 0 || s.MemMiB < 0 || s.DiskBytes < 0 || s.NetRx < 0 || s.NetTx < 0 {
		return fmt.Errorf("metrics: sample readings must be non-negative")
	}
	return nil
}

// PartitionTable names the time-partitioned table for a grain.
func PartitionTable(grain string) (string, error) {
	switch grain {
	case "1s":
		return "vm_metrics_1s", nil
	case "1m":
		return "vm_metrics_1m", nil
	default:
		return "", fmt.Errorf("metrics: unknown partition grain %q", grain)
	}
}
