// Package autoscale implements horizontal autoscaling: it polls project load
// (avg CPU% from the metrics store), and when a project has an AutoscalePolicy
// it scales the replica pool between MinReplicas and MaxReplicas with a
// cooldown between events.
package autoscale

import (
	"context"
	"log"
	"sync"
	"time"

	"porter/internal/types"
)

// Store is the persistence surface the scaler needs.
type Store interface {
	ListProjects() []*types.Project
	GetProject(id string) (*types.Project, bool)
	ListMetrics(vmID string, limit int) []*types.MetricSample
	PutProject(p *types.Project) error
}

// Scaler adjusts replica counts based on load.
type Scaler struct {
	store    Store
	boot     func(ctx context.Context, proj *types.Project, index int) // scale up hook
	stop     func(ctx context.Context, vmID string)                    // scale down hook
	interval time.Duration
	once     sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
	last     map[string]time.Time // projectID -> last scale event (cooldown)
}

// New returns an autoscaler that polls every interval.
func New(store Store, boot func(ctx context.Context, proj *types.Project, index int), stop func(ctx context.Context, vmID string), interval time.Duration) *Scaler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scaler{
		store:    store,
		boot:     boot,
		stop:     stop,
		interval: interval,
		stopCh:   make(chan struct{}),
		last:     map[string]time.Time{},
	}
}

// Start begins the scaling loop.
func (s *Scaler) Start() {
	s.once.Do(func() {
		s.wg.Add(1)
		go s.loop()
		log.Printf("autoscale: scaler running (every %s)", s.interval)
	})
}

// Stop halts the scaler.
func (s *Scaler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

func (s *Scaler) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.reconcile()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.reconcile()
		}
	}
}

func (s *Scaler) reconcile() {
	for _, proj := range s.store.ListProjects() {
		if proj == nil || proj.Autoscale == nil || !proj.Autoscale.Enabled {
			continue
		}
		p := proj.Autoscale
		if p.MaxReplicas <= 0 || p.MaxReplicas <= p.MinReplicas {
			continue
		}
		// Cooldown gate.
		if t, ok := s.last[proj.ID]; ok && time.Since(t) < cooldown(p.CooldownSec) {
			continue
		}
		avgCPU := s.avgCPU(proj)
		cur := len(proj.VMIDs)
		switch {
		case cur < p.MinReplicas:
			s.scale(proj, p.MinReplicas)
		case avgCPU > p.TargetCPU && cur < p.MaxReplicas:
			// Scale up by one replica per pass.
			s.scale(proj, cur+1)
		case p.ScaleDownCPU > 0 && avgCPU < p.ScaleDownCPU && cur > p.MinReplicas:
			s.scale(proj, cur-1)
		}
	}
}

// scale adjusts the project's replica pool to target via the boot/stop hooks.
func (s *Scaler) scale(proj *types.Project, target int) {
	cur := len(proj.VMIDs)
	for cur < target {
		if s.boot != nil {
			s.boot(context.Background(), proj, cur)
		}
		cur++
	}
	for cur > target {
		if cur-1 < len(proj.VMIDs) && s.stop != nil {
			s.stop(context.Background(), proj.VMIDs[cur-1])
		}
		proj.VMIDs = proj.VMIDs[:cur-1]
		cur--
	}
	proj.ReplicasDesired = target
	s.store.PutProject(proj)
	s.last[proj.ID] = time.Now()
	log.Printf("autoscale: %s scaled %d → %d (cpu %.1f%%)", proj.Name, len(proj.VMIDs), target, s.avgCPU(proj))
}

// avgCPU averages the latest cpu_percent metrics samples across the project's
// VMs. Returns 0 when no samples exist yet.
func (s *Scaler) avgCPU(proj *types.Project) float64 {
	var total, n float64
	for _, vid := range proj.VMIDs {
		for _, m := range s.store.ListMetrics(vid, 3) {
			if m.Metric != "cpu_percent" {
				continue
			}
			total += m.Value
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return total / n
}

func cooldown(sec int) time.Duration {
	if sec <= 0 {
		return time.Minute
	}
	return time.Duration(sec) * time.Second
}
