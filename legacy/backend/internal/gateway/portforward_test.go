package gateway

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"porter/internal/types"
)

// fakePortStore is a minimal Store with just enough to drive the forwarder.
type fakePortStore struct {
	mu  sync.RWMutex
	vms []*types.VM
}

func (f *fakePortStore) GetVM(id string) (*types.VM, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, v := range f.vms {
		if v.ID == id {
			return v, true
		}
	}
	return nil, false
}
func (f *fakePortStore) ListVMs() []*types.VM {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]*types.VM(nil), f.vms...)
}
func (f *fakePortStore) setVMs(vms ...*types.VM) {
	f.mu.Lock()
	f.vms = append([]*types.VM(nil), vms...)
	f.mu.Unlock()
}
func (f *fakePortStore) ListDomains(vmID string) []*types.Domain { return nil }
func (f *fakePortStore) ListFirewallRules(projectID string) []*types.FirewallRule {
	return nil
}
func (f *fakePortStore) AddTraffic(vmID string, e *types.TrafficEntry) {}

// TestTargetAddressUsesContainerPort guards the gateway upstream-address fix:
// the proxy must connect to the app's container port inside the VM.
func TestTargetAddressUsesContainerPort(t *testing.T) {
	vm := &types.VM{IPAddress: "10.42.0.5", Ports: []types.Port{
		{ContainerPort: 80, HostPort: 8080},
	}}
	u, ok := targetAddress(vm)
	if !ok {
		t.Fatal("targetAddress returned not-ok")
	}
	if u.Host != "10.42.0.5:80" {
		t.Fatalf("upstream should use container port 80, got %q", u.Host)
	}

	// No host port declared → default to container port.
	vm2 := &types.VM{IPAddress: "10.42.0.6", Ports: []types.Port{{ContainerPort: 3000}}}
	u2, _ := targetAddress(vm2)
	if u2.Host != "10.42.0.6:3000" {
		t.Fatalf("expected container port 3000, got %q", u2.Host)
	}
}

func reserveHostPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve host port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release reserved host port: %v", err)
	}
	return port
}

func responseThroughPort(client *http.Client, hostPort int) (string, error) {
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(hostPort) + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func waitForResponse(client *http.Client, hostPort int, want string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := responseThroughPort(client, hostPort)
		if err == nil && body == want {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func newResponseServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
}

func upstreamPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func testHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   time.Second,
	}
}

// TestPortForwarderBindsAndProxies verifies a host port reaches the running VM.
func TestPortForwarderBindsAndProxies(t *testing.T) {
	upstream := newResponseServer("pong")
	defer upstream.Close()
	upPort := upstreamPort(t, upstream)
	hostPort := reserveHostPort(t)
	store := &fakePortStore{}
	store.setVMs(&types.VM{
		ID: "vm1", State: types.StateRunning, IPAddress: "127.0.0.1",
		Ports: []types.Port{{ContainerPort: upPort, HostPort: hostPort}},
	})

	pf := NewPortForwarder(store)
	pf.Start()
	defer pf.Close()
	client := testHTTPClient()
	defer client.CloseIdleConnections()

	if !waitForResponse(client, hostPort, "pong") {
		t.Fatal("expected host-port request to reach the upstream VM")
	}
}

// TestPortForwarderRebindsWhenHostPortOwnerChanges ensures the listener is
// recreated for the new VM rather than retaining the original closure target.
func TestPortForwarderRebindsWhenHostPortOwnerChanges(t *testing.T) {
	oldUpstream := newResponseServer("old-vm")
	defer oldUpstream.Close()
	newUpstream := newResponseServer("new-vm")
	defer newUpstream.Close()
	hostPort := reserveHostPort(t)
	store := &fakePortStore{}
	store.setVMs(&types.VM{
		ID: "vm-old", State: types.StateRunning, IPAddress: "127.0.0.1",
		Ports: []types.Port{{ContainerPort: upstreamPort(t, oldUpstream), HostPort: hostPort}},
	})

	pf := NewPortForwarder(store)
	pf.Start()
	defer pf.Close()
	client := testHTTPClient()
	defer client.CloseIdleConnections()
	if !waitForResponse(client, hostPort, "old-vm") {
		t.Fatal("initial host-port listener did not reach the original VM")
	}

	store.setVMs(&types.VM{
		ID: "vm-new", State: types.StateRunning, IPAddress: "127.0.0.1",
		Ports: []types.Port{{ContainerPort: upstreamPort(t, newUpstream), HostPort: hostPort}},
	})
	pf.reconcile()

	if !waitForResponse(client, hostPort, "new-vm") {
		t.Fatal("host-port listener kept targeting the old VM after ownership changed")
	}
}

func TestPortForwarderRebindsWhenContainerPortChanges(t *testing.T) {
	oldUpstream := newResponseServer("old-target")
	defer oldUpstream.Close()
	newUpstream := newResponseServer("new-target")
	defer newUpstream.Close()
	hostPort := reserveHostPort(t)
	store := &fakePortStore{}
	vmID := "vm-same"
	store.setVMs(&types.VM{
		ID: vmID, State: types.StateRunning, IPAddress: "127.0.0.1",
		Ports: []types.Port{{ContainerPort: upstreamPort(t, oldUpstream), HostPort: hostPort}},
	})

	pf := NewPortForwarder(store)
	pf.Start()
	defer pf.Close()
	client := testHTTPClient()
	defer client.CloseIdleConnections()
	if !waitForResponse(client, hostPort, "old-target") {
		t.Fatal("initial listener did not reach the original target port")
	}

	store.setVMs(&types.VM{
		ID: vmID, State: types.StateRunning, IPAddress: "127.0.0.1",
		Ports: []types.Port{{ContainerPort: upstreamPort(t, newUpstream), HostPort: hostPort}},
	})
	pf.reconcile()
	if !waitForResponse(client, hostPort, "new-target") {
		t.Fatal("host-port listener kept targeting the old container port")
	}
}

func TestWantedPortsUsesDeterministicOldestClaim(t *testing.T) {
	const hostPort = 18080
	older := &types.VM{ID: "vm-z", State: types.StateRunning, IPAddress: "127.0.0.1", Ports: []types.Port{{ContainerPort: 8080, HostPort: hostPort}}}
	newer := &types.VM{ID: "vm-a", State: types.StateRunning, IPAddress: "127.0.0.1", Ports: []types.Port{{ContainerPort: 8081, HostPort: hostPort}}}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer.CreatedAt = older.CreatedAt.Add(time.Second)
	for _, order := range [][]*types.VM{{newer, older}, {older, newer}} {
		store := &fakePortStore{}
		store.setVMs(order...)
		target, ok := NewPortForwarder(store).wantedPorts()[hostPort]
		if !ok || target.vmID != older.ID || target.port != 8080 {
			t.Fatalf("conflicting host-port winner = %+v, ok=%v; want older %s:8080", target, ok, older.ID)
		}
	}
}

func TestWantedPortsUsesVMIDForEqualCreationTimes(t *testing.T) {
	const hostPort = 18081
	createdAt := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	vmZ := &types.VM{ID: "vm-z", State: types.StateRunning, IPAddress: "127.0.0.1", CreatedAt: createdAt, Ports: []types.Port{{ContainerPort: 8080, HostPort: hostPort}}}
	vmA := &types.VM{ID: "vm-a", State: types.StateRunning, IPAddress: "127.0.0.1", CreatedAt: createdAt, Ports: []types.Port{{ContainerPort: 8081, HostPort: hostPort}}}
	for _, order := range [][]*types.VM{{vmZ, vmA}, {vmA, vmZ}} {
		store := &fakePortStore{}
		store.setVMs(order...)
		target, ok := NewPortForwarder(store).wantedPorts()[hostPort]
		if !ok || target.vmID != vmA.ID || target.port != 8081 {
			t.Fatalf("equal-time conflict winner = %+v, ok=%v; want %s:8081", target, ok, vmA.ID)
		}
	}
}

// TestContainerPortHelper returns the declared container port.
func TestContainerPortHelper(t *testing.T) {
	vm := &types.VM{Ports: []types.Port{{ContainerPort: 5432, HostPort: 5432}}}
	if got := vm.ContainerPort(); got != 5432 {
		t.Fatalf("expected 5432, got %d", got)
	}
	vm2 := &types.VM{}
	if got := vm2.ContainerPort(); got != 0 {
		t.Fatalf("expected 0 for no ports, got %d", got)
	}
}
