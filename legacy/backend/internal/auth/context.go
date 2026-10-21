// Package auth contains shared credential primitives for database-backed
// Porter authentication. It deliberately does not know about HTTP, roles, or
// configuration fallbacks.
package auth

// TenantHeader carries the tenant scope for central (multi-tenant) callers.
// Scoped callers omit it; their tenant is bound to their credential.
const TenantHeader = "X-Tenant-ID"

// CentralTenant is the tenant claim value meaning "every tenant".
const CentralTenant = "*"

// AuthContext is the verified caller of one API request (task T3a).
// HTTP extraction lives with the caller (see internal/api); this struct stays
// transport-free so non-HTTP surfaces (CLI, workers) can build it too.
type AuthContext struct {
	// PrincipalType is user | service_account | agent.
	PrincipalType string

	// PrincipalID is the username / service-account id / agent id.
	PrincipalID string

	// ScopeType is platform | reseller | org | team | project | environment.
	ScopeType string

	// ScopeID identifies the scope node ('' with platform = whole platform).
	ScopeID string

	// Claims carries verified token claims (issuer, audience, expiry...).
	Claims map[string]string
}

// ResolveScope maps a tenant header value to a scope. Empty (or "*") means
// platform scope; otherwise the value names a tenant node whose type the
// caller supplies (tenancy wiring resolves it in T5).
func ResolveScope(scopeType, tenantHeader string) (string, string) {
	if tenantHeader == "" || tenantHeader == CentralTenant {
		return "platform", ""
	}
	if scopeType == "" {
		scopeType = "org"
	}
	return scopeType, tenantHeader
}
