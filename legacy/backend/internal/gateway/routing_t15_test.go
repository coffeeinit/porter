package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"porter/internal/types"
)

// t15gwStore is a hermetic Store + deploymentStore fake: project domains,
// legacy per-VM domains, and deployments are all explicit per test.
type t15gwStore struct {
	vms         []*types.VM
	projDomains map[string][]*types.Domain
	vmDomains   map[string][]*types.Domain
	deployments map[string][]*types.Deployment
	traffic     int
}

func (s *t15gwStore) GetVM(id string) (*types.VM, bool) {
	for _, vm := range s.vms {
		if vm.ID == id {
			return vm, true
		}
	}
	return nil, false
}

func (s *t15gwStore) ListVMs() []*types.VM { return s.vms }

func (s *t15gwStore) ListDomains(vmID string) []*types.Domain {
	return s.vmDomains[vmID]
}

func (s *t15gwStore) ListProjectDomains(projectID string) []*types.Domain {
	return s.projDomains[projectID]
}

func (s *t15gwStore) ListFirewallRules(projectID string) []*types.FirewallRule { return nil }

func (s *t15gwStore) AddTraffic(vmID string, e *types.TrafficEntry) { s.traffic++ }

func (s *t15gwStore) ListDeployments(projectID string) []*types.Deployment {
	return s.deployments[projectID]
}

func t15mkVM(id, project, ip string) *types.VM {
	return &types.VM{
		ID: id, Name: id, ProjectID: project,
		State: types.StateRunning, HealthStatus: types.HealthHealthy,
		IPAddress: ip,
	}
}

func t15ids(vms []*types.VM) map[string]bool {
	out := map[string]bool{}
	for _, vm := range vms {
		out[vm.ID] = true
	}
	return out
}

func TestT15PreviewExactBeatsWeightedProd(t *testing.T) {
	prod := t15mkVM("vm-prod", "p1", "10.0.0.1")
	prev := t15mkVM("vm-prev", "p1", "10.0.0.2")
	st := &t15gwStore{
		vms: []*types.VM{prod, prev},
		projDomains: map[string][]*types.Domain{
			"p1": {{ProjectID: "p1", Domain: "app.test", Type: "production", Status: "active"}},
		},
		deployments: map[string][]*types.Deployment{
			"p1": {
				{ID: "d-prod", ProjectID: "p1", RouteWeight: 100, BuildStatus: "ready", VMIDs: []string{"vm-prod"}},
				{ID: "d-prev", ProjectID: "p1", BuildStatus: "ready", PreviewURL: "prev-123.test", VMIDs: []string{"vm-prev"}},
			},
		},
	}
	gw := NewGateway(st)

	got := gw.backendsFor("prev-123.test")
	if len(got) != 1 || got[0].ID != "vm-prev" {
		t.Fatalf("preview-exact must win with only vm-prev, got %v", t15ids(got))
	}

	// Case-insensitive preview match.
	got = gw.backendsFor("PREV-123.TEST")
	if len(got) != 1 || got[0].ID != "vm-prev" {
		t.Fatalf("preview match must be case-insensitive, got %v", t15ids(got))
	}

	// The production domain uses the weighted pool, never the preview VM.
	got = gw.backendsFor("app.test")
	if ids := t15ids(got); !ids["vm-prod"] || ids["vm-prev"] {
		t.Fatalf("prod domain must serve weighted prod only, got %v", ids)
	}
}

func TestT15ProjectDomainsWeightedPool(t *testing.T) {
	vmA := t15mkVM("vm-a", "p1", "10.0.0.11")
	vmB := t15mkVM("vm-b", "p1", "10.0.0.12")
	st := &t15gwStore{
		vms: []*types.VM{vmA, vmB},
		projDomains: map[string][]*types.Domain{
			"p1": {{ProjectID: "p1", Domain: "app.test", Status: "active"}},
		},
		deployments: map[string][]*types.Deployment{
			"p1": {
				{ID: "d-a", ProjectID: "p1", RouteWeight: 50, BuildStatus: "ready", VMIDs: []string{"vm-a"}},
				{ID: "d-b", ProjectID: "p1", RouteWeight: 50, BuildStatus: "ready", VMIDs: []string{"vm-b"}},
			},
		},
	}
	gw := NewGateway(st)
	got := gw.backendsFor("app.test")
	ids := t15ids(got)
	if !ids["vm-a"] || !ids["vm-b"] {
		t.Fatalf("weighted pool must contain both deployments, got %v", ids)
	}
	if len(got) != 10 { // 50/10 = 5 repeats each
		t.Fatalf("expected 10 weighted entries (5+5), got %d", len(got))
	}

	// Legacy per-VM domain records still route (backward compat path).
	legacy := &t15gwStore{
		vms:       []*types.VM{t15mkVM("vm-l", "p9", "10.0.0.99")},
		vmDomains: map[string][]*types.Domain{"vm-l": {{Domain: "legacy.test"}}},
	}
	gwLegacy := NewGateway(legacy)
	got = gwLegacy.backendsFor("legacy.test")
	if len(got) != 1 || got[0].ID != "vm-l" {
		t.Fatalf("legacy per-VM domain must route, got %v", t15ids(got))
	}
}

type t15dnsFunc func(ctx context.Context, host string) ([]net.IP, error)

func (f t15dnsFunc) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return f(ctx, host)
}

func TestT15LocalDNSRouting(t *testing.T) {
	vm1 := t15mkVM("vm-1", "proj", "10.0.0.5")
	vm1.ServiceName = "web"
	vm2 := t15mkVM("vm-2", "proj", "10.0.0.6")
	vm2.ServiceName = "web"
	st := &t15gwStore{vms: []*types.VM{vm1, vm2}}
	gw := NewGateway(st)
	calls := 0
	gw.SetDNS(t15dnsFunc(func(_ context.Context, host string) ([]net.IP, error) {
		calls++
		if host == "web.proj.local" {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		}
		return nil, nil
	}))

	got := gw.backendsFor("web.proj.local")
	if len(got) != 1 || got[0].ID != "vm-1" {
		t.Fatalf("dns must pick the VM whose IP matches, got %v", t15ids(got))
	}
	if calls != 1 {
		t.Fatalf("expected 1 dns lookup, got %d", calls)
	}

	// Non-.local hosts never consult the resolver.
	before := calls
	_ = gw.backendsFor("web.example.com")
	if calls != before {
		t.Fatal("non-.local host must not trigger a DNS lookup")
	}
}

func TestT15UnknownHostFallback(t *testing.T) {
	empty := NewGateway(&t15gwStore{})
	if got := empty.backendsFor("nope.test"); len(got) != 0 {
		t.Fatalf("empty store must have no fallback, got %d", len(got))
	}
	req := httptest.NewRequest(http.MethodGet, "http://nope.test/", nil)
	rr := httptest.NewRecorder()
	empty.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown host with no backend must be 503, got %d", rr.Code)
	}

	// Single healthy VM answers any host (single-tenant dev convenience).
	single := NewGateway(&t15gwStore{vms: []*types.VM{t15mkVM("only", "p1", "10.0.0.7")}})
	if got := single.backendsFor("anything.test"); len(got) != 1 || got[0].ID != "only" {
		t.Fatalf("single-VM fallback must answer any host, got %v", t15ids(got))
	}

	// Unhealthy VMs are never fallback candidates.
	stopped := t15mkVM("s", "p1", "10.0.0.8")
	stopped.State = types.StateStopped
	sick := t15mkVM("u", "p1", "10.0.0.9")
	sick.HealthStatus = types.HealthUnhealthy
	none := NewGateway(&t15gwStore{vms: []*types.VM{stopped, sick}})
	if got := none.backendsFor("anything.test"); len(got) != 0 {
		t.Fatalf("unhealthy VMs must not serve fallback, got %v", t15ids(got))
	}
}

func TestT15CrossTenantUnknownHost(t *testing.T) {
	vmA := t15mkVM("vm-a", "pA", "10.0.0.21")
	vmB := t15mkVM("vm-b", "pB", "10.0.0.22")
	st := &t15gwStore{
		vms: []*types.VM{vmA, vmB},
		projDomains: map[string][]*types.Domain{
			"pA": {{ProjectID: "pA", Domain: "a.test", Status: "active"}},
			"pB": {{ProjectID: "pB", Domain: "b.test", Status: "active"}},
		},
	}
	gw := NewGateway(st)

	// Known hosts stay tenant-scoped.
	got := gw.backendsFor("a.test")
	if ids := t15ids(got); !ids["vm-a"] || ids["vm-b"] {
		t.Fatalf("known host a.test must serve only pA, got %v", ids)
	}
	got = gw.backendsFor("b.test")
	if ids := t15ids(got); !ids["vm-b"] || ids["vm-a"] {
		t.Fatalf("known host b.test must serve only pB, got %v", ids)
	}

	// Unknown hosts fall back across tenants (documented dev behavior).
	got = gw.backendsFor("unknown.test")
	if ids := t15ids(got); !ids["vm-a"] || !ids["vm-b"] {
		t.Fatalf("unknown-host fallback currently spans tenants, got %v", ids)
	}
}

func TestT15FailedBuildExcluded(t *testing.T) {
	good := t15mkVM("vm-good", "p1", "10.0.0.31")
	bad := t15mkVM("vm-bad", "p1", "10.0.0.32")
	other := t15mkVM("vm-other", "p2", "10.0.0.33")
	st := &t15gwStore{
		vms: []*types.VM{good, bad, other},
		projDomains: map[string][]*types.Domain{
			"p1": {{ProjectID: "p1", Domain: "app.test", Status: "active"}},
		},
		deployments: map[string][]*types.Deployment{
			"p1": {
				{ID: "d-good", ProjectID: "p1", RouteWeight: 100, BuildStatus: "ready", VMIDs: []string{"vm-good"}},
				{ID: "d-bad", ProjectID: "p1", RouteWeight: 100, BuildStatus: "failed", VMIDs: []string{"vm-bad"}, PreviewURL: "prev-bad.test"},
			},
		},
	}
	gw := NewGateway(st)

	// Weighted prod pool skips the failed build.
	got := gw.backendsFor("app.test")
	if ids := t15ids(got); !ids["vm-good"] || ids["vm-bad"] {
		t.Fatalf("failed build must be excluded from weighted pool, got %v", ids)
	}

	// Failed preview URL never resolves to its pool: the request falls
	// through to the cross-VM fallback instead of a 1-VM preview pool.
	got = gw.backendsFor("prev-bad.test")
	if len(got) != 3 {
		t.Fatalf("failed preview must fall through to fallback (3 healthy), got %v", t15ids(got))
	}
}

func TestT15WeightBounds(t *testing.T) {
	mk := func(id string) *types.VM { return t15mkVM(id, "p1", "10.0.0.40") }
	newGW := func(deps ...*types.Deployment) (*Gateway, *t15gwStore) {
		vms := []*types.VM{}
		for _, d := range deps {
			for _, vid := range d.VMIDs {
				vms = append(vms, mk(vid))
			}
		}
		st := &t15gwStore{vms: vms, deployments: map[string][]*types.Deployment{"p1": deps}}
		return NewGateway(st), st
	}
	fallback := mk("fb")

	// Zero weight with no rollout alias is excluded -> fallback.
	gw, _ := newGW(&types.Deployment{ID: "d0", ProjectID: "p1", BuildStatus: "ready", VMIDs: []string{"w0"}})
	if got := gw.weightedDeploymentBackends("p1", fallback); len(got) != 1 || got[0].ID != "fb" {
		t.Fatalf("zero-weight deployment must yield fallback, got %v", t15ids(got))
	}

	// Small weight clamps up to exactly 1 entry per VM.
	gw, _ = newGW(&types.Deployment{ID: "d5", ProjectID: "p1", RouteWeight: 5, BuildStatus: "ready", VMIDs: []string{"w5"}})
	if got := gw.weightedDeploymentBackends("p1", fallback); len(got) != 1 {
		t.Fatalf("weight 5 must clamp to 1 repeat, got %d", len(got))
	}

	// RolloutPercent is the legacy alias for RouteWeight: 80 -> 8 repeats.
	gw, _ = newGW(&types.Deployment{ID: "dr", ProjectID: "p1", RolloutPercent: 80, BuildStatus: "ready", VMIDs: []string{"wr"}})
	if got := gw.weightedDeploymentBackends("p1", fallback); len(got) != 8 {
		t.Fatalf("rollout 80 must give 8 repeats, got %d", len(got))
	}

	// Over-100 weights cap at 10 repeats per VM, never unbounded.
	gw, _ = newGW(&types.Deployment{ID: "dBig", ProjectID: "p1", RouteWeight: 1000, BuildStatus: "ready", VMIDs: []string{"wb"}})
	if got := gw.weightedDeploymentBackends("p1", fallback); len(got) != 10 {
		t.Fatalf("weight 1000 must cap at 10 repeats, got %d", len(got))
	}

	// Unhealthy members of a weighted deployment are skipped.
	gw, st := newGW(&types.Deployment{ID: "dMix", ProjectID: "p1", RouteWeight: 100, BuildStatus: "ready", VMIDs: []string{"ok", "down"}})
	for _, vm := range st.vms {
		if vm.ID == "down" {
			vm.State = types.StateStopped
		}
	}
	got := gw.weightedDeploymentBackends("p1", fallback)
	for _, vm := range got {
		if vm.ID == "down" {
			t.Fatal("stopped VM must be skipped in weighted pool")
		}
	}
	if len(got) != 10 {
		t.Fatalf("only the healthy VM contributes 10 repeats, got %d", len(got))
	}

	// String case: host match on the prod domain is case-insensitive.
	vm := t15mkVM("vm-c", "p1", "10.0.0.41")
	st2 := &t15gwStore{
		vms:         []*types.VM{vm},
		projDomains: map[string][]*types.Domain{"p1": {{ProjectID: "p1", Domain: "App.Test"}}},
	}
	if got := NewGateway(st2).backendsFor("app.test"); len(got) != 1 {
		t.Fatal("project domain match must be case-insensitive")
	}
	if !strings.EqualFold("App.Test", "app.test") {
		t.Fatal("sanity: EqualFold must hold")
	}
}
