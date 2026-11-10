package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresNodeCredentialsAndAgentHeartbeatIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_STORE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_STORE_TEST_POSTGRES_DSN to a disposable porter_test_store_* database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_store_") {
		t.Fatalf("refusing destructive integration test in database %q", databaseName)
	}
	if err := resetStoreIntegrationObjects(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetStoreIntegrationObjects(cleanupCtx, db); err != nil {
			t.Errorf("clean up disposable integration database: %v", err)
		}
	})
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply PostgreSQL schema through migration 0009: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	node, err := repo.CreateFleetNode(ctx, "credential-agent-node")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 5, 30, 0, 0, time.UTC)
	first, err := repo.ProvisionNodeCredential(ctx, node.ID, now, 5*time.Minute)
	if err != nil || first.Generation != 1 || len(first.Token) != 43 {
		t.Fatalf("first credential=%+v err=%v", first, err)
	}
	valid, err := repo.AuthenticateNodeCredential(ctx, node.ID, first.Token, now)
	if err != nil || !valid {
		t.Fatalf("authenticate first token=%t err=%v", valid, err)
	}
	valid, err = repo.AuthenticateNodeCredential(ctx, "another-node", first.Token, now)
	if err != nil || valid {
		t.Fatalf("cross-node credential auth=%t err=%v", valid, err)
	}
	var storedHash []byte
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM porter_control_plane.agent_node_credentials WHERE node_id = $1 AND generation = 1`, node.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	expectedHash := sha256.Sum256([]byte(first.Token))
	if len(storedHash) != len(expectedHash) || string(storedHash) != string(expectedHash[:]) || string(storedHash) == first.Token {
		t.Fatal("database did not persist only the token's SHA-256 hash")
	}

	if err := repo.RecordAgentHeartbeat(ctx, node.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("initial heartbeat: %v", err)
	}
	var state string
	var lastSeen time.Time
	if err := db.QueryRowContext(ctx, `SELECT state, last_seen_at FROM porter_control_plane.fleet_nodes WHERE id = $1`, node.ID).Scan(&state, &lastSeen); err != nil || state != string(v1.FleetNodeReady) || !lastSeen.Equal(now.Add(time.Second)) {
		t.Fatalf("offline node heartbeat state=%q last_seen=%v err=%v", state, lastSeen, err)
	}
	if err := repo.SetFleetNodeState(ctx, node.ID, v1.FleetNodeCordoned); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAgentHeartbeat(ctx, node.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1`, node.ID).Scan(&state); err != nil || state != string(v1.FleetNodeCordoned) {
		t.Fatalf("heartbeat incorrectly cleared cordoned state: %q err=%v", state, err)
	}

	rotationAt := now.Add(time.Minute)
	second, err := repo.ProvisionNodeCredential(ctx, node.ID, rotationAt, 5*time.Minute)
	if err != nil || second.Generation != 2 || second.PreviousValidUntil == nil || !second.PreviousValidUntil.Equal(rotationAt.Add(5*time.Minute)) {
		t.Fatalf("rotated credential=%+v err=%v", second, err)
	}
	valid, err = repo.AuthenticateNodeCredential(ctx, node.ID, first.Token, rotationAt.Add(4*time.Minute))
	if err != nil || !valid {
		t.Fatalf("previous credential should work during grace: valid=%t err=%v", valid, err)
	}
	valid, err = repo.AuthenticateNodeCredential(ctx, node.ID, first.Token, rotationAt.Add(5*time.Minute))
	if err != nil || valid {
		t.Fatalf("previous credential remained valid at grace expiry: valid=%t err=%v", valid, err)
	}
	valid, err = repo.AuthenticateNodeCredential(ctx, node.ID, second.Token, rotationAt.Add(5*time.Minute))
	if err != nil || !valid {
		t.Fatalf("current rotated credential invalid: valid=%t err=%v", valid, err)
	}
	if _, err := repo.ProvisionNodeCredential(ctx, "missing-node", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing-node credential error=%v, want not-found", err)
	}
	var credentialEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE fleet_node_id = $1 AND event_type LIKE 'agent.credential.%'`, node.ID).Scan(&credentialEvents); err != nil || credentialEvents != 2 {
		t.Fatalf("credential audit events=%d err=%v", credentialEvents, err)
	}
}
