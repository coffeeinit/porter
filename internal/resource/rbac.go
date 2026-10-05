// Package resource defines the canonical Porter domain types.
package resource

import "porter/internal/rbac"

// Scope type aliases: the rbac package is the authority for scope strings.
const (
	ScopePlatform    = rbac.ScopePlatform
	ScopeReseller    = rbac.ScopeReseller
	ScopeOrg         = rbac.ScopeOrg
	ScopeTeam        = rbac.ScopeTeam
	ScopeProject     = rbac.ScopeProject
	ScopeEnvironment = rbac.ScopeEnvironment
)

// Principal types for role assignments.
const (
	PrincipalUser           = "user"
	PrincipalServiceAccount = "service_account"
	PrincipalAgent          = "agent"
)

// Effect values for role→permission mapping. Deny overrides allow.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// RoleAssignment is the RBAC edge: which principal holds which role at which
// scope. It mirrors the role_assignments table (migration 0017).
type RoleAssignment struct {
	Metadata `json:"metadata"`
	Spec     RoleAssignmentSpec   `json:"spec"`
	Status   RoleAssignmentStatus `json:"status"`
}

func (r *RoleAssignment) GetMetadata() *Metadata { return &r.Metadata }
func (r *RoleAssignment) GetSpec() interface{}   { return r.Spec }
func (r *RoleAssignment) GetStatus() *Status     { return &r.Status.Status }
func (r *RoleAssignment) SetStatus(s *Status)    { r.Status.Status = *s }
func (r *RoleAssignment) GetKind() string        { return KindRoleAssignment }

// RoleAssignmentSpec is the desired state of a role assignment.
type RoleAssignmentSpec struct {
	// PrincipalType is user | service_account | agent.
	PrincipalType string `json:"principal_type"`

	// PrincipalID is the username / service-account id / agent id.
	PrincipalID string `json:"principal_id"`

	// ScopeType is platform | reseller | org | team | project | environment.
	ScopeType string `json:"scope_type"`

	// ScopeID identifies the scope node ('' with platform = whole platform).
	ScopeID string `json:"scope_id,omitempty"`

	// RoleID references roles(id).
	RoleID string `json:"role_id"`
}

// RoleAssignmentStatus is the observed state of a role assignment.
type RoleAssignmentStatus struct {
	Status `json:",inline"`

	// GrantedBy records who created the grant.
	GrantedBy string `json:"granted_by,omitempty"`
}

// Reseller is an optional capacity-reselling entity above organizations.
type Reseller struct {
	Metadata `json:"metadata"`
	Spec     ResellerSpec   `json:"spec"`
	Status   ResellerStatus `json:"status"`
}

func (r *Reseller) GetMetadata() *Metadata { return &r.Metadata }
func (r *Reseller) GetSpec() interface{}   { return r.Spec }
func (r *Reseller) GetStatus() *Status     { return &r.Status.Status }
func (r *Reseller) SetStatus(s *Status)    { r.Status.Status = *s }
func (r *Reseller) GetKind() string        { return KindReseller }

// ResellerSpec is the desired state of a reseller.
type ResellerSpec struct {
	// Name is the reseller name.
	Name string `json:"name"`
}

// ResellerStatus is the observed state of a reseller.
type ResellerStatus struct {
	Status `json:",inline"`

	// OrganizationCount is the number of organizations under this reseller.
	OrganizationCount int `json:"organization_count,omitempty"`
}

// Team reuses the canonical type in org.go (TeamSpec.OrgID links the org);
// the teams table (migration 0017) persists the same shape with an optional
// reseller link carried as a label (reseller_id) until the spec grows it.

// ScopeChain returns the ancestor scope chain for a scope, innermost first.
// Grants at any entry apply to the queried scope (deny overrides allow).
func ScopeChain(scopeType, scopeID string) [][2]string {
	chain := [][2]string{{scopeType, scopeID}}
	// Ancestor walk requires parent lookups which live in the store; the
	// engine resolves full chains via Store.RoleScopeAncestors. This helper
	// covers the static type ordering: environment → project → team → org →
	// reseller → platform.
	order := []string{ScopeEnvironment, ScopeProject, ScopeTeam, ScopeOrg, ScopeReseller, ScopePlatform}
	idx := -1
	for i, t := range order {
		if t == scopeType {
			idx = i
			break
		}
	}
	if idx < 0 {
		return [][2]string{{ScopePlatform, ""}}
	}
	for _, t := range order[idx+1:] {
		chain = append(chain, [2]string{t, ""})
	}
	return chain
}
