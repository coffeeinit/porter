// Meter pump (OCM-22 remainder): turns host-side samples into durable
// usage meters. Sampler and Sink are interfaces: the host sampler reads
// cgroups on Linux, tests inject fakes, and nothing ever fabricates a
// reading — a sampler error skips the VM for that tick, loudly in logs.
package metrics

import (
	"fmt"
	"log"
	"time"
)

// Sampler reads one host-side sample for a VM.
type Sampler interface {
	Sample(vmID string) (Sample, error)
}

// MeterSink records one metered quantity (store.RecordUsage shape).
type MeterSink interface {
	RecordUsage(projectID, resourceRef, meter string, quantity float64, unit, idempotencyKey string)
}

// Pump ticks a VM set: sample → validate → record cpu + memory meters with
// per-tick idempotency keys (replayable, deduplicated downstream).
type Pump struct {
	Sample  Sampler
	Sink    MeterSink
	Project string // default project when ProjectFor is nil
	// ProjectFor attributes meters per VM (main wires store lookup).
	ProjectFor func(vmID string) string
}

func (p Pump) projectFor(vmID string) string {
	if p.ProjectFor != nil {
		if proj := p.ProjectFor(vmID); proj != "" {
			return proj
		}
	}
	return p.Project
}

// PumpOnce runs a single tick over vmIDs, returning per-VM errors.
func (p Pump) PumpOnce(vmIDs []string, tick time.Time) []error {
	var errs []error
	for _, id := range vmIDs {
		s, err := p.Sample.Sample(id)
		if err != nil {
			errs = append(errs, fmt.Errorf("metrics: sample %s: %w", id, err))
			continue
		}
		s.At = tick
		if err := s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("metrics: sample %s: %w", id, err))
			continue
		}
		key := fmt.Sprintf("pump|%s|%d", id, tick.Unix())
		proj := p.projectFor(id)
		p.Sink.RecordUsage(proj, id, "vcpu.millicores", s.Micros, "millicores", key)
		p.Sink.RecordUsage(proj, id, "mem.mib", s.MemMiB, "mib", key)
		p.Sink.RecordUsage(proj, id, "disk.bytes", float64(s.DiskBytes), "bytes", key)
		p.Sink.RecordUsage(proj, id, "net.rx.bytes", float64(s.NetRx), "bytes", key)
		p.Sink.RecordUsage(proj, id, "net.tx.bytes", float64(s.NetTx), "bytes", key)
	}
	if len(errs) > 0 {
		log.Printf("metrics: pump tick %d errors", len(errs))
	}
	return errs
}
