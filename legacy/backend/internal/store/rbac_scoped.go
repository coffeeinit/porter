package store

import (
	"context"
	"log"

	"porter/internal/rbac"
)

// This file implements rbac.Resolver over Postgres plus scoped assignment
// management (task T2b). It bridges migration 0007 (roles/permissions/
// role_permissions + effect) with migration 0017 (role_assignments).

// AssignmentsForPrincipal returns every role assignment for the principal.
func (s *Store) AssignmentsForPrincipal(ctx context.Context, principalType, principalID string) ([]rbac.Assignment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT principal_type, principal_id, scope_type, scope_id, role_id
		   FROM role_assignments WHERE principal_type = $1 AND principal_id = $2`,
		principalType, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rbac.Assignment
	for rows.Next() {
		var a rbac.Assignment
		if err := rows.Scan(&a.PrincipalType, &a.PrincipalID, &a.Scope.Type, &a.Scope.ID, &a.RoleID); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RoleCapabilities returns the capability entries (with effects) for roles.
func (s *Store) RoleCapabilities(ctx context.Context, roleIDs []string) ([]rbac.RoleCapability, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT permission_id, COALESCE(effect, 'allow') FROM role_permissions WHERE role_id = ANY($1)`,
		roleIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rbac.RoleCapability
	for rows.Next() {
		var c rbac.RoleCapability
		if err := rows.Scan(&c.Capability, &c.Effect); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ScopeAncestors returns ancestor scopes innermost-first (excluding itself),
// walking environments → projects → groups(teams) → orgs → platform.
func (s *Store) ScopeAncestors(ctx context.Context, scope rbac.Scope) ([]rbac.Scope, error) {
	var out []rbac.Scope
	push := func(t, id string) {
		if id != "" {
			out = append(out, rbac.Scope{Type: t, ID: id})
		}
	}
	switch scope.Type {
	case rbac.ScopeEnvironment:
		var projectID string
		if err := s.pool.QueryRow(ctx, `SELECT project_id::text FROM environments WHERE id::text = $1`, scope.ID).Scan(&projectID); err == nil {
			push(rbac.ScopeProject, projectID)
			rest, err := s.ScopeAncestors(ctx, rbac.Scope{Type: rbac.ScopeProject, ID: projectID})
			if err == nil {
				out = append(out, rest...)
			}
		}
	case rbac.ScopeProject:
		// Parent groups (teams) then org (via groups or projects.org_id).
		rows, err := s.pool.Query(ctx, `SELECT group_id::text FROM group_projects WHERE project_id::text = $1`, scope.ID)
		if err == nil {
			for rows.Next() {
				var gid string
				if err := rows.Scan(&gid); err == nil {
					push(rbac.ScopeTeam, gid)
					var orgID string
					if err := s.pool.QueryRow(ctx, `SELECT org_id::text FROM groups WHERE id::text = $1`, gid).Scan(&orgID); err == nil {
						push(rbac.ScopeOrg, orgID)
					}
				}
			}
			rows.Close()
		}
		var orgID string
		if err := s.pool.QueryRow(ctx, `SELECT org_id::text FROM projects WHERE id::text = $1`, scope.ID).Scan(&orgID); err == nil && orgID != "" {
			push(rbac.ScopeOrg, orgID)
		}
	case rbac.ScopeTeam:
		var orgID string
		if err := s.pool.QueryRow(ctx, `SELECT org_id::text FROM groups WHERE id::text = $1`, scope.ID).Scan(&orgID); err == nil {
			push(rbac.ScopeOrg, orgID)
		}
		if err := s.pool.QueryRow(ctx, `SELECT organization_id FROM teams WHERE id = $1`, scope.ID).Scan(&orgID); err == nil {
			push(rbac.ScopeOrg, orgID)
		}
	case rbac.ScopeOrg:
		// orgs have no parent below platform.
	case rbac.ScopeReseller:
		// resellers have no parent below platform.
	}
	out = append(out, rbac.Scope{Type: rbac.ScopePlatform, ID: ""})
	return dedupeScopes(out), nil
}

func dedupeScopes(in []rbac.Scope) []rbac.Scope {
	seen := map[rbac.Scope]bool{}
	var out []rbac.Scope
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// AssignRole grants a role to a principal at a scope (idempotent).
func (s *Store) AssignRole(principalType, principalID, scopeType, scopeID, roleID, grantedBy string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role_id, granted_by)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (principal_type, principal_id, scope_type, scope_id, role_id) DO NOTHING`,
		principalType, principalID, scopeType, scopeID, roleID, grantedBy)
	if err != nil {
		log.Printf("store: assign role: %v", err)
	}
	return err
}

// RevokeRole removes a role grant.
func (s *Store) RevokeRole(principalType, principalID, scopeType, scopeID, roleID string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM role_assignments
		  WHERE principal_type=$1 AND principal_id=$2 AND scope_type=$3 AND scope_id=$4 AND role_id=$5`,
		principalType, principalID, scopeType, scopeID, roleID)
	if err != nil {
		log.Printf("store: revoke role: %v", err)
	}
	return err
}

// ListAssignmentsByScope lists grants at one scope node.
func (s *Store) ListAssignmentsByScope(scopeType, scopeID string) []map[string]string {
	rows, err := s.pool.Query(context.Background(),
		`SELECT principal_type, principal_id, role_id FROM role_assignments
		  WHERE scope_type=$1 AND scope_id=$2 ORDER BY principal_type, principal_id`,
		scopeType, scopeID)
	if err != nil {
		log.Printf("store: list assignments: %v", err)
		return nil
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var pt, pid, rid string
		if err := rows.Scan(&pt, &pid, &rid); err != nil {
			continue
		}
		out = append(out, map[string]string{"principal_type": pt, "principal_id": pid, "role_id": rid})
	}
	return out
}

// HasCapability resolves scoped capability checks (task T2 entry point).
func (s *Store) HasCapability(principalType, principalID, capability, scopeType, scopeID string) bool {
	ok, err := rbac.HasCapability(context.Background(), s, principalType, principalID, capability,
		rbac.Scope{Type: scopeType, ID: scopeID})
	if err != nil {
		log.Printf("store: HasCapability: %v", err)
		return false
	}
	return ok
}

// ScopedDeny reports whether any matching scoped grant explicitly denies the
// capability. It lets legacy checks stay permissive while honoring deny.
func (s *Store) ScopedDeny(principalType, principalID, capability, scopeType, scopeID string) bool {
	ctx := context.Background()
	assignments, err := s.AssignmentsForPrincipal(ctx, principalType, principalID)
	if err != nil || len(assignments) == 0 {
		return false
	}
	scope := rbac.Scope{Type: scopeType, ID: scopeID}
	ancestors, err := s.ScopeAncestors(ctx, scope)
	if err != nil {
		return false
	}
	inScope := func(aScope rbac.Scope) bool {
		if aScope == scope {
			return true
		}
		for _, o := range ancestors {
			if o == aScope {
				return true
			}
		}
		return false
	}
	var roleIDs []string
	for _, a := range assignments {
		if inScope(a.Scope) {
			roleIDs = append(roleIDs, a.RoleID)
		}
	}
	if len(roleIDs) == 0 {
		return false
	}
	rows, err := s.pool.Query(ctx,
		`SELECT 1 FROM role_permissions WHERE role_id = ANY($1) AND permission_id = $2 AND effect = 'deny' LIMIT 1`,
		roleIDs, capability)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}
