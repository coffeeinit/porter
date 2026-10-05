package dns

import (
	"context"
	"testing"

	"porter/internal/types"
)

// legacyStore is a minimal store for the old Resolver tests.
type legacyStore struct{ vms []*types.VM }

func (s *legacyStore) GetVM(id string) (*types.VM, bool) {
	for _, v := range s.vms {
		if v.ID == id {
			return v, true
		}
	}
	return nil, false
}
func (s *legacyStore) ListVMs() []*types.VM { return s.vms }

func legacyVM(id, svc, project, ip string) *types.VM {
	return &types.VM{ID: id, ServiceName: svc, ProjectID: project, Name: project, IPAddress: ip}
}

func TestLookupIP(t *testing.T) {
	r := New(&legacyStore{vms: []*types.VM{
		legacyVM("1", "web", "myapp", "10.0.0.5"),
		legacyVM("2", "db", "myapp", "10.0.0.6"),
	}})
	ips, err := r.LookupIP(context.Background(), "web.myapp.local")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0].String() != "10.0.0.5" {
		t.Fatalf("unexpected ips: %v", ips)
	}
}

func TestLookupIPUnknownService(t *testing.T) {
	r := New(&legacyStore{vms: []*types.VM{legacyVM("1", "web", "myapp", "10.0.0.5")}})
	if _, err := r.LookupIP(context.Background(), "cache.myapp.local"); err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestLookupIPRejectsNonLocalHost(t *testing.T) {
	r := New(&legacyStore{})
	if _, err := r.LookupIP(context.Background(), "example.com"); err == nil {
		t.Fatal("expected error for non-.local host")
	}
}

func TestLookupHostReverse(t *testing.T) {
	r := New(&legacyStore{vms: []*types.VM{legacyVM("1", "web", "myapp", "10.0.0.5")}})
	name, err := r.LookupHost(context.Background(), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if name != "web.myapp.local" {
		t.Fatalf("unexpected name %q", name)
	}
	if _, err := r.LookupHost(context.Background(), "10.0.0.99"); err == nil {
		t.Fatal("expected error for unknown ip")
	}
}
