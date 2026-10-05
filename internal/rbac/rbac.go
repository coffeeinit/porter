// Package rbac implements the Porter capability resolution engine (task T2).
//
// Effective permissions = union of capabilities from every role assigned to the
// principal at the queried scope or any ancestor scope, with DENY overriding
// ALLOW. Resolution never hardcodes role strings: callers pass a capability
// (permissions.id, e.g. "project.read") and a scope, and the engine answers.
//
// The engine is storage-agnostic: it reads through the Resolver interface
// (implemented by internal/store over Postgres, faked in tests).
package rbac

import (
	"context"
	"fmt"
)

// Scope types for role assignments. Grants inherit downward:
// platform → reseller → org → team → project → environment.
const (
	ScopePlatform    = "platform"
	ScopeReseller    = "reseller"
	ScopeOrg         = "org"
	ScopeTeam        = "team"
	ScopeProject     = "project"
	ScopeEnvironment = "environment"
)

// Effect values for role→capability mapping. Deny overrides allow.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// Scope is one node in the tenancy tree.
type Scope struct {
	Type string // platform | reseller | org | team | project | environment
	ID   string // '' with platform = whole platform
}

// Assignment is one role grant to a principal at a scope.
type Assignment struct {
	PrincipalType string // user | service_account | agent
	PrincipalID   string
	Scope         Scope
	RoleID        string
}

// RoleCapability is one capability entry of a role with its effect.
type RoleCapability struct {
	Capability string // permissions.id, e.g. "project.read"
	Effect     string // allow | deny
}

// Resolver supplies the data HasCapability needs. Implementations must be safe
// for concurrent use.
type Resolver interface {
	// AssignmentsForPrincipal returns every role assignment for the principal.
	AssignmentsForPrincipal(ctx context.Context, principalType, principalID string) ([]Assignment, error)
	// RoleCapabilities returns the capability entries (with effects) for roles.
	RoleCapabilities(ctx context.Context, roleIDs []string) ([]RoleCapability, error)
	// ScopeAncestors returns the ancestor scopes of a scope, innermost first
	// (excluding the scope itself).
	ScopeAncestors(ctx context.Context, scope Scope) ([]Scope, error)
}

// HasCapability reports whether the principal holds the capability at the scope
// or any ancestor scope. Deny at any matching scope defeats allows.
func HasCapability(ctx context.Context, r Resolver, principalType, principalID, capability string, scope Scope) (bool, error) {
	if principalType == "" || principalID == "" || capability == "" {
		return false, fmt.Errorf("rbac: principal type, principal id and capability are required")
	}
	assignments, err := r.AssignmentsForPrincipal(ctx, principalType, principalID)
	if err != nil {
		return false, fmt.Errorf("rbac: assignments: %w", err)
	}
	if len(assignments) == 0 {
		return false, nil
	}
	ancestors, err := r.ScopeAncestors(ctx, scope)
	if err != nil {
		return false, fmt.Errorf("rbac: ancestors: %w", err)
	}
	matching := map[string]bool{}
	for _, a := range assignments {
		if a.Scope == scope || scopeIn(a.Scope, ancestors) {
			matching[a.RoleID] = true
		}
	}
	if len(matching) == 0 {
		return false, nil
	}
	roleIDs := make([]string, 0, len(matching))
	for id := range matching {
		roleIDs = append(roleIDs, id)
	}
	caps, err := r.RoleCapabilities(ctx, roleIDs)
	if err != nil {
		return false, fmt.Errorf("rbac: role capabilities: %w", err)
	}
	allowed, denied := false, false
	for _, c := range caps {
		if c.Capability != capability {
			continue
		}
		switch c.Effect {
		case EffectDeny:
			denied = true
		case EffectAllow:
			allowed = true
		default:
			// Unknown effect strings never grant: fail closed.
		}
	}
	if denied {
		return false, nil
	}
	return allowed, nil
}

func scopeIn(s Scope, list []Scope) bool {
	for _, o := range list {
		if o == s {
			return true
		}
	}
	return false
}
