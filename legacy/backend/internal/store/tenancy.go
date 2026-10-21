package store

import (
	"context"
	"log"

	"porter/internal/rbac"
)

// This file wires the tenancy scope tree end-to-end (task T5):
// org → team(group) → project → environment. Membership resolution reuses
// org_members / project_members; team(group) scope inherits the org membership
// role until dedicated team-member rows exist (migration 0018 or later).

// ScopeChainForProject returns the org and group (team) parents of a project.
func (s *Store) ScopeChainForProject(projectID string) (orgID string, groupIDs []string) {
	if projectID == "" {
		return "", nil
	}
	_ = s.pool.QueryRow(context.Background(),
		`SELECT org_id::text FROM projects WHERE id::text = $1`, projectID).Scan(&orgID)
	rows, err := s.pool.Query(context.Background(),
		`SELECT group_id::text FROM group_projects WHERE project_id::text = $1`, projectID)
	if err != nil {
		log.Printf("store: scope chain for project: %v", err)
		return orgID, nil
	}
	defer rows.Close()
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err == nil {
			groupIDs = append(groupIDs, gid)
		}
	}
	return orgID, groupIDs
}

// OrgForGroup returns the org owning a group (team).
func (s *Store) OrgForGroup(groupID string) string {
	var orgID string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT org_id::text FROM groups WHERE id::text = $1`, groupID).Scan(&orgID); err != nil {
		return ""
	}
	return orgID
}

// ProjectForEnvironment returns the project owning an environment.
func (s *Store) ProjectForEnvironment(envID string) string {
	var projectID string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT project_id::text FROM environments WHERE id::text = $1`, envID).Scan(&projectID); err != nil {
		return ""
	}
	return projectID
}

// GroupRoleForUser resolves a user's role in a group (team) through the
// owning org's membership. Returns "" when there is no membership.
func (s *Store) GroupRoleForUser(groupID, username string) string {
	return s.OrgRoleForUser(s.OrgForGroup(groupID), username)
}

// UserOrgIDs lists the orgs a user belongs to.
func (s *Store) UserOrgIDs(username string) []string {
	u, ok := s.GetUserByUsername(username)
	if !ok {
		return nil
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT org_id::text FROM org_members WHERE user_id = $1`, u.ID)
	if err != nil {
		log.Printf("store: user orgs: %v", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// UserScopes lists every scope node a principal reaches: direct project
// memberships plus orgs with their groups and projects. Used by scoped list
// filtering (T4b). Project creators land here via their membership row.
func (s *Store) UserScopes(username string) []rbac.Scope {
	var out []rbac.Scope
	u, ok := s.GetUserByUsername(username)
	if ok {
		rows, err := s.pool.Query(context.Background(),
			`SELECT project_id::text FROM project_members WHERE user_id = $1`, u.ID)
		if err == nil {
			for rows.Next() {
				var pid string
				if err := rows.Scan(&pid); err == nil {
					out = append(out, rbac.Scope{Type: rbac.ScopeProject, ID: pid})
				}
			}
			rows.Close()
		}
	}
	for _, orgID := range s.UserOrgIDs(username) {
		out = append(out, rbac.Scope{Type: rbac.ScopeOrg, ID: orgID})
		rows, err := s.pool.Query(context.Background(),
			`SELECT id::text FROM groups WHERE org_id::text = $1`, orgID)
		if err != nil {
			continue
		}
		var groups []string
		for rows.Next() {
			var gid string
			if err := rows.Scan(&gid); err == nil {
				groups = append(groups, gid)
			}
		}
		rows.Close()
		for _, gid := range groups {
			out = append(out, rbac.Scope{Type: rbac.ScopeTeam, ID: gid})
			prows, err := s.pool.Query(context.Background(),
				`SELECT project_id::text FROM group_projects WHERE group_id::text = $1`, gid)
			if err != nil {
				continue
			}
			for prows.Next() {
				var pid string
				if err := prows.Scan(&pid); err == nil {
					out = append(out, rbac.Scope{Type: rbac.ScopeProject, ID: pid})
				}
			}
			prows.Close()
		}
	}
	return out
}
