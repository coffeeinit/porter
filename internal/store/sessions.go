package store

import (
	"context"
	"log"
	"time"
)

// This file implements DB-backed sessions (task T3) and team membership
// (task T5): auth_sessions rows for opaque session tokens, team_members rows
// for per-team roles. GroupRoleForUser checks team rows first, then falls back
// to the owning org's membership (documented inheritance).

// Session is one opaque session token row.
type Session struct {
	ID            string
	PrincipalType string
	PrincipalID   string
	ScopeType     string
	ScopeID       string
	ExpiresAt     time.Time
}

// CreateSession inserts a session with a pre-hashed token, returning its id.
func (s *Store) CreateSession(principalType, principalID, tokenHash, scopeType, scopeID string, ttl time.Duration) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO auth_sessions (id, principal_type, principal_id, token_hash, scope_type, scope_id, expires_at)
		 VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, now() + ($6 || ' seconds')::interval) RETURNING id`,
		principalType, principalID, tokenHash, scopeType, scopeID, int64(ttl.Seconds())).Scan(&id)
	if err != nil {
		log.Printf("store: create session: %v", err)
	}
	return id, err
}

// GetSessionByToken resolves a raw token to its session when valid
// (hash match, unrevoked, unexpired).
func (s *Store) GetSessionByToken(tokenHash string) (Session, bool) {
	var sess Session
	var revoked *time.Time
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, principal_type, principal_id, scope_type, scope_id, expires_at, revoked_at
		   FROM auth_sessions WHERE token_hash = $1`,
		tokenHash).Scan(&sess.ID, &sess.PrincipalType, &sess.PrincipalID,
		&sess.ScopeType, &sess.ScopeID, &sess.ExpiresAt, &revoked)
	if err != nil {
		return Session{}, false
	}
	if revoked != nil || time.Now().UTC().After(sess.ExpiresAt) {
		return Session{}, false
	}
	return sess, true
}

// RevokeSession marks a session revoked by token hash.
func (s *Store) RevokeSession(tokenHash string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE auth_sessions SET revoked_at = now() WHERE token_hash = $1`, tokenHash)
	return err
}

// AddTeamMember grants a team (group) role to a user (idempotent).
func (s *Store) AddTeamMember(groupID, userID, role string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO team_members (group_id, user_id, role) VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (group_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		groupID, userID, role)
	if err != nil {
		log.Printf("store: add team member: %v", err)
	}
	return err
}

// RemoveTeamMember revokes a team membership.
func (s *Store) RemoveTeamMember(groupID, userID string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM team_members WHERE group_id::text = $1 AND user_id = $2`, groupID, userID)
	return err
}

// TeamRoleForUser resolves a user's team role: direct team_members row first,
// then the owning org's membership (inheritance).
func (s *Store) TeamRoleForUser(groupID, username string) string {
	if groupID == "" || username == "" {
		return ""
	}
	var role string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT tm.role FROM team_members tm
		   JOIN users u ON u.username = tm.user_id OR u.id::text = tm.user_id
		  WHERE tm.group_id::text = $1 AND (u.username = $2) LIMIT 1`,
		groupID, username).Scan(&role); err == nil && role != "" {
		return role
	}
	return s.GroupRoleForUser(groupID, username)
}

// TeamMember is one team membership row.
type TeamMember struct {
	GroupID  string
	UserID   string
	Username string
	Role     string
}

// ListTeamMembers returns a team's memberships with usernames resolved.
func (s *Store) ListTeamMembers(groupID string) []TeamMember {
	rows, err := s.pool.Query(context.Background(),
		`SELECT tm.group_id::text, tm.user_id, COALESCE(u.username, tm.user_id), tm.role
		 FROM team_members tm LEFT JOIN users u
		   ON u.username = tm.user_id OR u.id::text = tm.user_id
		 WHERE tm.group_id::text = $1 ORDER BY 3`, groupID)
	if err != nil {
		log.Printf("store: list team members: %v", err)
		return nil
	}
	defer rows.Close()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.GroupID, &m.UserID, &m.Username, &m.Role); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}
