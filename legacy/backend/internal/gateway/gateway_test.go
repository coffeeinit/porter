package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"porter/internal/types"
)

// fakeStore implements the gateway Store surface in memory.
type fakeStore struct {
	vms     []*types.VM
	domains map[string][]*types.Domain
	rules   []*types.FirewallRule
	traffic []*types.TrafficEntry
}

func (f *fakeStore) GetVM(id string) (*types.VM, bool) {
	for _, vm := range f.vms {
		if vm.ID == id {
			return vm, true
		}
	}
	return nil, false
}
func (f *fakeStore) ListVMs() []*types.VM                    { return f.vms }
func (f *fakeStore) ListDomains(vmID string) []*types.Domain { return f.domains[vmID] }
func (f *fakeStore) ListProjectDomains(projectID string) []*types.Domain {
	return f.domains[projectID]
}
func (f *fakeStore) ListFirewallRules(p string) []*types.FirewallRule {
	return f.rules
}
func (f *fakeStore) AddTraffic(vmID string, e *types.TrafficEntry) {
	f.traffic = append(f.traffic, e)
}

// ListProjectDomains satisfies Store for fakePortStore (pre-existing gap:
// the Store interface requires it but portforward_test.go's fake was never
// updated, breaking the package's test build). Defined here because
// portforward_test.go is outside this task's edit scope.
func (f *fakePortStore) ListProjectDomains(projectID string) []*types.Domain { return nil }

func healthyVM(id, ip string, ports ...int) *types.VM {
	var ps []types.Port
	for _, p := range ports {
		ps = append(ps, types.Port{ContainerPort: p, HostPort: p})
	}
	return &types.VM{
		ID: id, Name: id, State: types.StateRunning,
		HealthStatus: types.HealthHealthy, IPAddress: ip, Ports: ps,
	}
}

func TestTrafficRingCapsAndOrder(t *testing.T) {
	ring := NewTrafficRing(3)
	for i := 0; i < 10; i++ {
		ring.Add("vm1", &types.TrafficEntry{Method: "GET", Path: "/"})
	}
	got := ring.List("vm1", 0)
	if len(got) != 3 {
		t.Fatalf("expected ring capped at 3, got %d", len(got))
	}
	if got[0].Path != "/" {
		t.Fatal("expected oldest-first order")
	}
}

func TestGatewayProxiesAndRecordsTraffic(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	st := &fakeStore{vms: []*types.VM{healthyVM("vm1", host, port)}}
	gw := NewGateway(st)

	req := httptest.NewRequest(http.MethodGet, "http://web.myapp.test/", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if len(st.traffic) != 1 {
		t.Fatalf("expected 1 store traffic entry, got %d", len(st.traffic))
	}
	if st.traffic[0].Host != "web.myapp.test" {
		t.Fatalf("unexpected traffic host %q", st.traffic[0].Host)
	}
	if got := len(gw.Ring().List("vm1", 0)); got != 1 {
		t.Fatalf("expected 1 ring entry, got %d", got)
	}
}

func TestGatewayFirewallBlocksDeny(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	st := &fakeStore{
		vms: []*types.VM{healthyVM("vm1", host, port)},
		rules: []*types.FirewallRule{
			{ID: "r1", ProjectID: "proj1", Action: "deny", Source: "203.0.113.0/24", Priority: 1, Active: true},
		},
	}
	// Make the VM part of project proj1 so the rule applies.
	st.vms[0].ProjectID = "proj1"
	gw := NewGateway(st)

	req := httptest.NewRequest(http.MethodGet, "http://web.myapp.test/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from firewall, got %d", rr.Code)
	}

	// A source outside the denied CIDR passes through.
	req2 := httptest.NewRequest(http.MethodGet, "http://web.myapp.test/", nil)
	req2.RemoteAddr = "198.51.100.7:1234"
	rr2 := httptest.NewRecorder()
	gw.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed source, got %d", rr2.Code)
	}
}

func TestGatewayNoBackendReturns503(t *testing.T) {
	gw := NewGateway(&fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "http://nobody.test/", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestGatewayScaleToZeroWake(t *testing.T) {
	stopped := healthyVM("vm1", "10.0.0.9")
	stopped.State = types.StateStopped
	stopped.ProjectID = "proj1"
	st := &fakeStore{
		vms:     []*types.VM{stopped},
		domains: map[string][]*types.Domain{"proj1": {{ProjectID: "proj1", Domain: "app.test", Type: "custom", Status: "active"}}},
	}
	gw := NewGateway(st)
	var woke string
	gw.Waker = func(_ context.Context, projectID string) error {
		woke = projectID
		return nil
	}

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while waking, got %d", rr.Code)
	}
	if woke != "proj1" {
		t.Fatalf("expected waker called with proj1, got %q", woke)
	}
	if rr.Header().Get("Retry-After") != "5" {
		t.Fatalf("expected Retry-After: 5, got %q", rr.Header().Get("Retry-After"))
	}
	if rr.Header().Get("X-Porter-Wake") != "started" {
		t.Fatalf("expected X-Porter-Wake: started, got %q", rr.Header().Get("X-Porter-Wake"))
	}
}

func TestGatewayNoWakeWithoutWaker(t *testing.T) {
	stopped := healthyVM("vm1", "10.0.0.9")
	stopped.State = types.StateStopped
	stopped.ProjectID = "proj1"
	st := &fakeStore{
		vms:     []*types.VM{stopped},
		domains: map[string][]*types.Domain{"proj1": {{ProjectID: "proj1", Domain: "app.test", Type: "custom", Status: "active"}}},
	}
	gw := NewGateway(st) // no Waker: current 503 behavior

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	if rr.Header().Get("X-Porter-Wake") != "" {
		t.Fatalf("expected no X-Porter-Wake header without a waker, got %q", rr.Header().Get("X-Porter-Wake"))
	}
}

func TestGatewayNoWakeWhenHealthy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	vm := healthyVM("vm1", host, port)
	vm.ProjectID = "proj1"
	st := &fakeStore{
		vms:     []*types.VM{vm},
		domains: map[string][]*types.Domain{"proj1": {{ProjectID: "proj1", Domain: "app.test", Type: "custom", Status: "active"}}},
	}
	gw := NewGateway(st)
	woke := false
	gw.Waker = func(_ context.Context, _ string) error {
		woke = true
		return nil
	}

	req := httptest.NewRequest(http.MethodGet, "http://app.test/", nil)
	rr := httptest.NewRecorder()
	gw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if woke {
		t.Fatal("waker must not fire while a healthy backend serves the host")
	}
}

type dnsResolverFunc func(context.Context, string) ([]net.IP, error)

func (f dnsResolverFunc) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return f(ctx, host)
}

func TestGatewayResolvesLocalDNS(t *testing.T) {
	st := &fakeStore{vms: []*types.VM{healthyVM("vm1", "10.0.0.5")}}
	gw := NewGateway(st)
	gw.SetDNS(dnsResolverFunc(func(_ context.Context, host string) ([]net.IP, error) {
		if host == "web.myproj.local" {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		}
		return nil, nil
	}))

	vms := gw.backendsFor("web.myproj.local")
	if len(vms) != 1 || vms[0].ID != "vm1" {
		t.Fatalf("expected vm1 via dns, got %+v", vms)
	}
}
