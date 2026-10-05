// Package dns provides an embedded authoritative resolver for the
// `<svc>.<project>.local` and `<svc>-<n>.<project>.local` zones. VMs register
// their IP against `<svc>.<project>.local`; lookups resolve to the replica
// pool. PTR (host -> name) lookups return the svc name for a VM IP.
package dns

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"porter/internal/types"
)

// Store is the narrow persistence surface the resolver needs.
type Store interface {
	GetVM(id string) (*types.VM, bool)
	ListVMs() []*types.VM
}

var zonePattern = regexp.MustCompile(`^([a-z0-9-]+)\.([a-z0-9-]+)\.local$`)

// Resolver resolves .local service names to VM IPs.
type Resolver struct {
	store Store
}

// New builds a resolver over the store.
func New(store Store) *Resolver {
	return &Resolver{store: store}
}

// LookupIP resolves a host like `web.myapp.local` (or `web-0.myapp.local`) to
// the IPs of the matching VMs. Returns an error when nothing matches.
func (r *Resolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	m := zonePattern.FindStringSubmatch(host)
	if m == nil {
		return nil, fmt.Errorf("dns: %s is not a .local service name", host)
	}
	svc, project := m[1], m[2]

	var out []net.IP
	for _, vm := range r.store.ListVMs() {
		if vm == nil {
			continue
		}
		if !strings.EqualFold(vm.ServiceName, svc) {
			continue
		}
		if project != "" && !strings.Contains(strings.ToLower(vm.ProjectID), project) &&
			!strings.Contains(strings.ToLower(vm.Name), project) {
			continue
		}
		ip := net.ParseIP(vm.IPAddress)
		if ip != nil {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dns: no VM for %s", host)
	}
	return out, nil
}

// LookupHost maps a VM IP back to its `<svc>.<project>.local` name.
func (r *Resolver) LookupHost(ctx context.Context, ip string) (string, error) {
	for _, vm := range r.store.ListVMs() {
		if vm != nil && vm.IPAddress == ip {
			return fmt.Sprintf("%s.%s.local", vm.ServiceName, projectName(vm)), nil
		}
	}
	return "", fmt.Errorf("dns: no VM with ip %s", ip)
}

func projectName(vm *types.VM) string {
	if vm.ProjectID == "" {
		return "default"
	}
	return vm.ProjectID
}
