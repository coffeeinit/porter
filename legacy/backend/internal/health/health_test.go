package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"porter/internal/types"
)

type memStore struct {
	vms map[string]*types.VM
}

func (s *memStore) GetVM(id string) (*types.VM, bool) {
	v, ok := s.vms[id]
	return v, ok
}
func (s *memStore) PutVM(vm *types.VM) { s.vms[vm.ID] = vm }

type hubStub struct{}

func (h *hubStub) Broadcast(event string, data any) {}

func TestProbeHTTPHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, port := parseHostPort(t, srv.URL)
	vm := &types.VM{IPAddress: host, Ports: []types.Port{{ContainerPort: port}}}
	ck := New(&memStore{}, &hubStub{}, nil)
	if !ck.Probe(context.Background(), vm, HealthSpec{Type: "http", Path: "/"}) {
		t.Fatal("expected healthy http probe")
	}
}

func TestProbeHTTPDown(t *testing.T) {
	vm := &types.VM{IPAddress: "127.0.0.1", Ports: []types.Port{{ContainerPort: 1}}}
	ck := New(&memStore{}, &hubStub{}, nil)
	if ck.Probe(context.Background(), vm, HealthSpec{Type: "http", Path: "/"}) {
		t.Fatal("expected unhealthy http probe")
	}
}

func TestProbeTCPHealthy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	vm := &types.VM{IPAddress: "127.0.0.1", Ports: []types.Port{{ContainerPort: port}}}
	ck := New(&memStore{}, &hubStub{}, nil)
	if !ck.Probe(context.Background(), vm, HealthSpec{Type: "tcp"}) {
		t.Fatal("expected healthy tcp probe")
	}
}

func TestProbeOnceReplacesUnhealthy(t *testing.T) {
	s := &memStore{vms: map[string]*types.VM{"v1": {ID: "v1", State: types.StateRunning, IPAddress: "127.0.0.1"}}}
	var replaced int32
	ck := New(s, &hubStub{}, func(_ context.Context, vmID string) {
		atomic.AddInt32(&replaced, 1)
	})
	ck.probeOnce(context.Background(), "v1", HealthSpec{Type: "tcp", Port: 1})

	// onReplace runs in a goroutine — wait for it.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&replaced) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("expected onReplace to fire for unhealthy vm")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.vms["v1"].HealthStatus != types.HealthUnhealthy {
		t.Fatal("expected vm marked unhealthy")
	}
}

func TestProbeOnceHealthyNoReplace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, port := parseHostPort(t, srv.URL)
	s := &memStore{vms: map[string]*types.VM{"v1": {ID: "v1", State: types.StateRunning, IPAddress: host}}}
	var replaced int32
	ck := New(s, &hubStub{}, func(_ context.Context, vmID string) {
		atomic.AddInt32(&replaced, 1)
	})
	ck.probeOnce(context.Background(), "v1", HealthSpec{Type: "http", Path: "/", Port: port})
	if atomic.LoadInt32(&replaced) != 0 {
		t.Fatal("expected no replace for healthy vm")
	}
}

func parseHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u := raw
	// strip scheme
	hostPort := u
	if len(u) > 7 && u[:7] == "http://" {
		hostPort = u[7:]
	}
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("split %s: %v", hostPort, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
