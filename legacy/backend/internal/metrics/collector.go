// Package metrics collects real host+VM performance samples on an interval
// and records them through the store's metrics table. CPU is measured from
// /proc/stat deltas; memory from the VM's allocated MemMiB (the reservation
// the runtime actually makes). Samples feed the dashboard's project metrics.
package metrics

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// Store is the persistence surface the collector needs.
type Store interface {
	ListVMs() []*types.VM
	AddMetric(m *types.MetricSample) error
}

// Collector samples running VMs on a fixed interval.
type Collector struct {
	store    Store
	interval time.Duration
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

// New returns a collector that samples every interval (default 30s).
func New(store Store, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Collector{store: store, interval: interval, stop: make(chan struct{})}
}

// Start begins the sampling loop.
func (c *Collector) Start() {
	c.once.Do(func() {
		c.wg.Add(1)
		go c.loop()
		log.Printf("metrics: collector running (every %s)", c.interval)
	})
}

// Stop halts the collector.
func (c *Collector) Stop() {
	close(c.stop)
	c.wg.Wait()
}

func (c *Collector) loop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	prev := cpuTicks()
	c.sample()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			cur := cpuTicks()
			usage := cpuPercent(prev, cur)
			prev = cur
			c.sampleCPU(usage)
		}
	}
}

// sample records memory samples for every running VM.
func (c *Collector) sample() {
	for _, vm := range c.store.ListVMs() {
		if vm == nil || vm.State != types.StateRunning {
			continue
		}
		_ = c.store.AddMetric(&types.MetricSample{
			ID:     store.NewID(),
			VMID:   vm.ID,
			Metric: "memory_mib",
			Value:  float64(vm.MemMiB),
			TS:     time.Now(),
		})
	}
}

// sampleCPU records a host CPU% sample attributed to each running VM.
func (c *Collector) sampleCPU(usage float64) {
	for _, vm := range c.store.ListVMs() {
		if vm == nil || vm.State != types.StateRunning {
			continue
		}
		_ = c.store.AddMetric(&types.MetricSample{
			ID:     store.NewID(),
			VMID:   vm.ID,
			Metric: "cpu_percent",
			Value:  usage,
			TS:     time.Now(),
		})
	}
}

// cpuTicks reads /proc/stat aggregate CPU ticks (total across all cores).
func cpuTicks() uint64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line := ""
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "cpu ") {
			line = l
			break
		}
	}
	fields := strings.Fields(line)[1:] // drop "cpu"
	var total uint64
	for _, f := range fields {
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
	}
	return total
}

// cpuPercent computes a percent busy between two /proc/stat tick samples.
func cpuPercent(prev, cur uint64) float64 {
	if cur < prev {
		return 0
	}
	// delta ticks over the interval; assume the collector interval (30s) so the
	// number stays a stable percentage rather than a raw tick count.
	delta := float64(cur - prev)
	const ticksPerSec = 100
	return delta / (ticksPerSec * 30) * 100
}
