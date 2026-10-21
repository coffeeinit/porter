package autoscale

import (
	"context"
	"testing"
	"time"

	"porter/internal/types"
)

// memStore implements the Store surface in memory.
type memStore struct {
	projects []*types.Project
	metrics  map[string][]*types.MetricSample
}

func (m *memStore) ListProjects() []*types.Project { return m.projects }
func (m *memStore) GetProject(id string) (*types.Project, bool) {
	for _, p := range m.projects {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}
func (m *memStore) ListMetrics(vmID string, limit int) []*types.MetricSample {
	return m.metrics[vmID]
}
func (m *memStore) PutProject(p *types.Project) error {
	for i, x := range m.projects {
		if x.ID == p.ID {
			m.projects[i] = p
			return nil
		}
	}
	m.projects = append(m.projects, p)
	return nil
}

func cpuSamples(vmID string, vals ...float64) []*types.MetricSample {
	out := make([]*types.MetricSample, 0, len(vals))
	for _, v := range vals {
		out = append(out, &types.MetricSample{VMID: vmID, Metric: "cpu_percent", Value: v, TS: time.Now()})
	}
	return out
}

func TestScaleUpAboveTarget(t *testing.T) {
	st := &memStore{
		projects: []*types.Project{{
			ID: "p1", Name: "app", VMIDs: []string{"v1"},
			ReplicasDesired: 1,
			Autoscale: &types.AutoscalePolicy{
				MinReplicas: 1, MaxReplicas: 3, TargetCPU: 80, Enabled: true,
			},
		}},
		metrics: map[string][]*types.MetricSample{"v1": cpuSamples("v1", 90, 95)},
	}
	boots, stops := 0, 0
	sc := New(st,
		func(_ context.Context, p *types.Project, _ int) { boots++ },
		func(_ context.Context, _ string) { stops++ },
		time.Hour) // long poll; we drive reconcile directly
	sc.reconcile()

	proj, _ := st.GetProject("p1")
	if boots != 1 {
		t.Fatalf("expected 1 scale-up boot, got %d", boots)
	}
	if proj.ReplicasDesired != 2 {
		t.Fatalf("expected desired=2, got %d", proj.ReplicasDesired)
	}
}

func TestNoScaleBelowTargetCPU(t *testing.T) {
	st := &memStore{
		projects: []*types.Project{{
			ID: "p1", Name: "app", VMIDs: []string{"v1"},
			ReplicasDesired: 1,
			Autoscale: &types.AutoscalePolicy{
				MinReplicas: 1, MaxReplicas: 3, TargetCPU: 80, Enabled: true,
			},
		}},
		metrics: map[string][]*types.MetricSample{"v1": cpuSamples("v1", 20, 30)},
	}
	boots := 0
	sc := New(st, func(_ context.Context, _ *types.Project, _ int) { boots++ }, func(_ context.Context, _ string) {}, time.Hour)
	sc.reconcile()
	if boots != 0 {
		t.Fatalf("expected no scale-up, got %d boots", boots)
	}
}

func TestCooldownBlocksRescale(t *testing.T) {
	st := &memStore{
		projects: []*types.Project{{
			ID: "p1", Name: "app", VMIDs: []string{"v1"},
			ReplicasDesired: 1,
			Autoscale: &types.AutoscalePolicy{
				MinReplicas: 1, MaxReplicas: 3, TargetCPU: 80, CooldownSec: 3600, Enabled: true,
			},
		}},
		metrics: map[string][]*types.MetricSample{"v1": cpuSamples("v1", 95)},
	}
	boots := 0
	sc := New(st, func(_ context.Context, _ *types.Project, _ int) { boots++ }, func(_ context.Context, _ string) {}, time.Hour)
	sc.reconcile()
	if boots != 1 {
		t.Fatalf("expected first scale-up, got %d", boots)
	}
	sc.reconcile() // within cooldown — must not scale again
	if boots != 1 {
		t.Fatalf("expected cooldown to block rescale, got %d boots", boots)
	}
}
