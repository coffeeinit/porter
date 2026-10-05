// Per-workload tunnel records and orphan reaping (OCM-09 remainder).
// Provider is parameterized (cloudflare today, direct/proxy tomorrow);
// Cloudflare stays optional per the product invariant. The reaper lists
// provider tunnels by prefix and returns orphans for idempotent delete.
package gateway

import (
	"fmt"
	"strings"
)

// Tunnel providers.
const (
	TunnelCloudflare = "cloudflare"
	TunnelDirect     = "direct"
)

// Tunnel is one workload's edge attachment.
type Tunnel struct {
	ID       string // provider tunnel id (ocm-vm- style prefix per provider)
	Hostname string
	Provider string
}

// Validate gates tunnel records (provider allowlist, non-empty identity).
func (t Tunnel) Validate() error {
	switch t.Provider {
	case TunnelCloudflare, TunnelDirect:
	default:
		return fmt.Errorf("gateway: unknown tunnel provider %q", t.Provider)
	}
	if t.ID == "" || t.Hostname == "" {
		return fmt.Errorf("gateway: tunnel needs id and hostname")
	}
	return nil
}

// Reap returns provider tunnels with the managed prefix that have no live
// workload behind them. Delete is idempotent downstream (404 = success), so
// re-listing after a partial sweep is safe.
func Reap(prefix string, providerTunnels []Tunnel, liveHostnames map[string]bool) []Tunnel {
	orphans := []Tunnel{}
	for _, t := range providerTunnels {
		if !strings.HasPrefix(t.ID, prefix) {
			continue
		}
		if !liveHostnames[t.Hostname] {
			orphans = append(orphans, t)
		}
	}
	return orphans
}
