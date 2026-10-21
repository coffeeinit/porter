package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"porter/internal/types"
)

// fakePortStore is a minimal Store with just enough to drive the forwarder.
type fakePortStore struct {
	vms []*types.VM
}

func (f *fakePortStore) GetVM(id string) (*types.VM, bool) {
	for _, v := range f.vms {
		if v.ID == id {
			return v, true
		}
	}
	return nil, false
}
func (f *fakePortStore) ListVMs() []*types.VM                    { return f.vms }
func (f *fakePortStore) ListDomains(vmID string) []*types.Domain { return nil }
func (f *fakePortStore) ListFirewallRules(projectID string) []*types.FirewallRule {
	return nil
}
func (f *fakePortStore) AddTraffic(vmID string, e *types.TrafficEntry) {}

// TestTargetAddressUsesContainerPort guards the gateway upstream-address fix:
// the proxy must connect to the app's container port *inside* the VM, not the
// host-facing HostPort.
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

// TestPortForwarderBindsAndProxies starts an upstream echo HTTP server, tells
// the forwarder a running VM claims a host port mapping to it, then verifies a
// connection through the bound host port reaches the upstream.
func TestPortForwarderBindsAndProxies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer upstream.Close()

	_, portStr, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	upPort, _ := strconv.Atoi(portStr)

	// Reserve a host port by binding it first so we know it's free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	hostPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // free it; the forwarder will bind it

	store := &fakePortStore{vms: []*types.VM{
		{
			ID:        "vm1",
			State:     types.StateRunning,
			IPAddress: "127.0.0.1",
			Ports:     []types.Port{{ContainerPort: upPort, HostPort: hostPort}},
		},
	}}

	pf := NewPortForwarder(store)
	pf.Start()
	defer pf.Close()

	// Give the reconcile loop a moment to bind.
	deadline := time.Now().Add(3 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(hostPort) + "/")
		if err == nil {
			b := make([]byte, 16)
			n, _ := resp.Body.Read(b)
			_ = resp.Body.Close()
			body = string(b[:n])
			if body == "pong" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body != "pong" {
		t.Fatalf("expected 'pong' through host port, got %q", body)
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
