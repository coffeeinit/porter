package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"testing"
)

func TestMCPTokenLifecycleAndAuditPersistence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	credential, raw, err := s.CreateMCPToken(ctx, "test-agent", []string{"apps:read"}, []string{"project-1"}, []string{"app-1"}, nil, nil)
	if err != nil {
		t.Fatalf("create MCP token: %v", err)
	}
	if raw == "" || credential.ID == "" {
		t.Fatalf("created credential is incomplete: credential=%+v", credential)
	}
	var storedHash string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM mcp_tokens WHERE id=?`, credential.ID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored token hash: %v", err)
	}
	if storedHash != hashToken(raw) || storedHash == raw {
		t.Fatalf("raw bearer was not stored as a one-way hash")
	}

	loaded, ok, err := s.AuthenticateMCPToken(ctx, raw)
	if err != nil || !ok {
		t.Fatalf("authenticate active token: ok=%v err=%v", ok, err)
	}
	if loaded.ID != credential.ID || len(loaded.Capabilities) != 1 || loaded.Capabilities[0] != "apps:read" {
		t.Fatalf("authenticated principal metadata mismatch: %+v", loaded)
	}
	if _, ok, err := s.AuthenticateMCPToken(ctx, "ptr_mcp_invalid"); err != nil || ok {
		t.Fatalf("invalid token authenticated: ok=%v err=%v", ok, err)
	}

	if err := s.RecordMCPToolCall(ctx, credential.ID, "porter_get_autoscale", "started"); err != nil {
		t.Fatalf("record MCP actor: %v", err)
	}
	var actorID, tool, outcome string
	if err := s.db.QueryRowContext(ctx, `SELECT actor_id,tool,outcome FROM mcp_audit ORDER BY created_at DESC LIMIT 1`).Scan(&actorID, &tool, &outcome); err != nil {
		t.Fatalf("read MCP audit record: %v", err)
	}
	if actorID != credential.ID || tool != "porter_get_autoscale" || outcome != "started" {
		t.Fatalf("unexpected MCP audit record: actor=%q tool=%q outcome=%q", actorID, tool, outcome)
	}

	if err := s.RevokeMCPToken(ctx, credential.ID); err != nil {
		t.Fatalf("revoke MCP token: %v", err)
	}
	if _, ok, err := s.AuthenticateMCPToken(ctx, raw); err != nil || ok {
		t.Fatalf("revoked token authenticated: ok=%v err=%v", ok, err)
	}
	if err := s.RevokeMCPToken(ctx, credential.ID); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("second revoke error = %v, want ErrMCPTokenNotFound", err)
	}
}

func TestMCPTokenExpiryAndLegacySchemaMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Rebuild the credential table in its pre-expiry shape, then reopen.
	rawDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rawDB.Exec(`DROP TABLE mcp_tokens; CREATE TABLE mcp_tokens(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,capabilities TEXT NOT NULL,project_ids TEXT NOT NULL,app_ids TEXT NOT NULL,node_ids TEXT NOT NULL,created_at TIMESTAMP NOT NULL,revoked_at TIMESTAMP NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	rawDB.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("legacy migration: %v", err)
	}
	defer s.Close()
	future := time.Now().UTC().Add(time.Hour)
	cred, raw, err := s.CreateMCPToken(ctx, "expires", []string{"apps:read"}, nil, nil, nil, &future)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListMCPTokens(ctx)
	if err != nil || len(listed) != 1 || listed[0].ExpiresAt == nil {
		t.Fatalf("expiry missing from list: %+v err=%v", listed, err)
	}
	if _, ok, err := s.AuthenticateMCPToken(ctx, raw); err != nil || !ok {
		t.Fatalf("future token not accepted: ok=%v err=%v", ok, err)
	}
	past := time.Now().UTC().Add(-time.Second)
	_, expiredRaw, err := s.CreateMCPToken(ctx, "expired", []string{"apps:read"}, nil, nil, nil, &past)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.AuthenticateMCPToken(ctx, expiredRaw); err != nil || ok {
		t.Fatalf("expired token accepted: ok=%v err=%v", ok, err)
	}
	_ = cred
}
