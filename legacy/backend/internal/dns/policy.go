// Split-horizon DNS policy: the internal DNS server answers workload
// service-discovery names for in-cluster clients and refuses them for the
// outside world, while public zones resolve identically everywhere. Views
// are pure policy — the server enforces them at query time.
package dns

import (
	"fmt"
	"strings"
)

// Views.
const (
	ViewInternal = "internal" // cluster clients: service discovery enabled
	ViewExternal = "external" // everyone else: public zones only
)

// Policy is one zone's visibility rule.
type Policy struct {
	Zone     string // e.g. svc.cluster.local (internal) or example.com (public)
	Internal bool   // true = internal view only
}

// Validate gates policies: zone must be a bare suffix, no wildcards here.
func (p Policy) Validate() error {
	z := strings.ToLower(strings.TrimSuffix(p.Zone, "."))
	if z == "" || strings.ContainsAny(z, " *\\/") {
		return fmt.Errorf("dns: bad policy zone %q", p.Zone)
	}
	return nil
}

// Visible reports whether zone answers in view. Unknown zones fall closed
// (not visible) so a missing policy never leaks internal names.
func Visible(policies []Policy, zone, view string) bool {
	z := strings.ToLower(strings.TrimSuffix(zone, "."))
	for _, p := range policies {
		if strings.ToLower(strings.TrimSuffix(p.Zone, ".")) != z {
			continue
		}
		if p.Internal {
			return view == ViewInternal
		}
		return true
	}
	return false
}

// SearchDomains returns per-project DNS search suffixes: the project's own
// service zone first, then the shared cluster zone.
func SearchDomains(project string) []string {
	project = strings.ToLower(strings.TrimSpace(project))
	if project == "" {
		return []string{"svc.cluster.local."}
	}
	return []string{project + ".svc.cluster.local.", "svc.cluster.local."}
}
