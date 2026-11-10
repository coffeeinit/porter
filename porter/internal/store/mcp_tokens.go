package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrMCPTokenNotFound = errors.New("MCP token not found")

// MCPToken contains non-secret credential metadata. The raw bearer token is
// returned only by CreateMCPToken and is never stored or listed.
type MCPToken struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Capabilities []string   `json:"capabilities"`
	ProjectIDs   []string   `json:"project_ids"`
	AppIDs       []string   `json:"app_ids"`
	NodeIDs      []string   `json:"node_ids"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

func (s *Store) migrateMCP() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS mcp_tokens(
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 token_hash TEXT NOT NULL UNIQUE,
 capabilities TEXT NOT NULL,
 project_ids TEXT NOT NULL,
 app_ids TEXT NOT NULL,
 node_ids TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL,
 revoked_at TIMESTAMP NULL,
 expires_at TIMESTAMP NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_tokens_active ON mcp_tokens(token_hash, revoked_at);
CREATE TABLE IF NOT EXISTS mcp_audit(
 id TEXT PRIMARY KEY,
 actor_id TEXT NOT NULL,
 tool TEXT NOT NULL,
 outcome TEXT NOT NULL,
 created_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_audit_created ON mcp_audit(created_at DESC);
`)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`PRAGMA table_info(mcp_tokens)`)
	if err != nil {
		return err
	}
	hasExpiry := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "expires_at" {
			hasExpiry = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !hasExpiry {
		_, err = s.db.Exec(`ALTER TABLE mcp_tokens ADD COLUMN expires_at TIMESTAMP NULL`)
	}
	return err
}

func (s *Store) CreateMCPToken(ctx context.Context, name string, capabilities, projectIDs, appIDs, nodeIDs []string, expiresAt *time.Time) (MCPToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return MCPToken{}, "", errors.New("name is required")
	}
	var err error
	if capabilities, err = normalizeMCPValues(capabilities, false); err != nil {
		return MCPToken{}, "", fmt.Errorf("capabilities: %w", err)
	}
	if len(capabilities) == 0 {
		return MCPToken{}, "", errors.New("at least one capability is required")
	}
	if projectIDs, err = normalizeMCPValues(projectIDs, true); err != nil {
		return MCPToken{}, "", fmt.Errorf("project_ids: %w", err)
	}
	if appIDs, err = normalizeMCPValues(appIDs, true); err != nil {
		return MCPToken{}, "", fmt.Errorf("app_ids: %w", err)
	}
	if nodeIDs, err = normalizeMCPValues(nodeIDs, true); err != nil {
		return MCPToken{}, "", fmt.Errorf("node_ids: %w", err)
	}

	raw, err := randomToken("ptr_mcp_")
	if err != nil {
		return MCPToken{}, "", err
	}
	id := NewID("mcp")
	createdAt := time.Now().UTC()
	capsJSON, _ := json.Marshal(capabilities)
	projectsJSON, _ := json.Marshal(projectIDs)
	appsJSON, _ := json.Marshal(appIDs)
	nodesJSON, _ := json.Marshal(nodeIDs)
	_, err = s.db.ExecContext(ctx, `INSERT INTO mcp_tokens
(id,name,token_hash,capabilities,project_ids,app_ids,node_ids,created_at,expires_at)
VALUES(?,?,?,?,?,?,?,?,?)`, id, name, hashToken(raw), string(capsJSON), string(projectsJSON), string(appsJSON), string(nodesJSON), createdAt, expiresAt)
	if err != nil {
		return MCPToken{}, "", err
	}
	return MCPToken{ID: id, Name: name, Capabilities: capabilities, ProjectIDs: projectIDs, AppIDs: appIDs, NodeIDs: nodeIDs, CreatedAt: createdAt, ExpiresAt: expiresAt}, raw, nil
}

func (s *Store) AuthenticateMCPToken(ctx context.Context, raw string) (MCPToken, bool, error) {
	if raw == "" {
		return MCPToken{}, false, nil
	}
	var token MCPToken
	var capsJSON, projectsJSON, appsJSON, nodesJSON string
	var revokedAt, expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,name,capabilities,project_ids,app_ids,node_ids,created_at,revoked_at,expires_at
FROM mcp_tokens WHERE token_hash=? AND revoked_at IS NULL`, hashToken(raw)).Scan(
		&token.ID, &token.Name, &capsJSON, &projectsJSON, &appsJSON, &nodesJSON, &token.CreatedAt, &revokedAt, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPToken{}, false, nil
	}
	if err != nil {
		return MCPToken{}, false, err
	}
	if err := json.Unmarshal([]byte(capsJSON), &token.Capabilities); err != nil {
		return MCPToken{}, false, fmt.Errorf("decode MCP capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(projectsJSON), &token.ProjectIDs); err != nil {
		return MCPToken{}, false, fmt.Errorf("decode MCP project scope: %w", err)
	}
	if err := json.Unmarshal([]byte(appsJSON), &token.AppIDs); err != nil {
		return MCPToken{}, false, fmt.Errorf("decode MCP app scope: %w", err)
	}
	if err := json.Unmarshal([]byte(nodesJSON), &token.NodeIDs); err != nil {
		return MCPToken{}, false, fmt.Errorf("decode MCP node scope: %w", err)
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Time
	}
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
		if !expiresAt.Time.After(time.Now().UTC()) {
			return MCPToken{}, false, nil
		}
	}
	return token, true, nil
}

func (s *Store) ListMCPTokens(ctx context.Context) ([]MCPToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,capabilities,project_ids,app_ids,node_ids,created_at,revoked_at,expires_at
FROM mcp_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MCPToken, 0)
	for rows.Next() {
		var token MCPToken
		var capsJSON, projectsJSON, appsJSON, nodesJSON string
		var revokedAt, expiresAt sql.NullTime
		if err := rows.Scan(&token.ID, &token.Name, &capsJSON, &projectsJSON, &appsJSON, &nodesJSON, &token.CreatedAt, &revokedAt, &expiresAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capsJSON), &token.Capabilities); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(projectsJSON), &token.ProjectIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(appsJSON), &token.AppIDs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(nodesJSON), &token.NodeIDs); err != nil {
			return nil, err
		}
		if revokedAt.Valid {
			token.RevokedAt = &revokedAt.Time
		}
		if expiresAt.Valid {
			token.ExpiresAt = &expiresAt.Time
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

func (s *Store) RevokeMCPToken(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMCPTokenNotFound
	}
	return nil
}

// RecordMCPToolCall stores the actor, tool name, and outcome, but never the
// arguments or result, which could contain credentials or application data.
func (s *Store) RecordMCPToolCall(ctx context.Context, actorID, tool, outcome string) error {
	if actorID == "" || tool == "" {
		return errors.New("actor and tool are required")
	}
	if len(outcome) > 40 {
		outcome = outcome[:40]
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_audit(id,actor_id,tool,outcome,created_at) VALUES(?,?,?,?,?)`, NewID("mcpaudit"), actorID, tool, outcome, time.Now().UTC())
	return err
}

func normalizeMCPValues(values []string, allowWildcard bool) ([]string, error) {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("values cannot be empty")
		}
		if value == "*" && !allowWildcard {
			return nil, errors.New("wildcard is not allowed")
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}
