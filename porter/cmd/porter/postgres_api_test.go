package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDisabledPostgresAPIDoesNotFallbackToSQLiteHandler(t *testing.T) {
	legacyCalls := 0
	legacy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("legacy sqlite handler"))
	})
	handler, closeDB, err := configureAPIRoutes(legacy, "operator-token", "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()

	v1Response := httptest.NewRecorder()
	handler.ServeHTTP(v1Response, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if v1Response.Code != http.StatusServiceUnavailable || legacyCalls != 0 {
		t.Fatalf("disabled v1 response status=%d legacyCalls=%d body=%s", v1Response.Code, legacyCalls, v1Response.Body.String())
	}
	var envelope v1.ErrorResponse
	if err := json.Unmarshal(v1Response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != v1.ErrorFailedPrecondition || envelope.Error.RequestID == "" || v1Response.Header().Get("X-Request-ID") != envelope.Error.RequestID {
		t.Fatalf("disabled v1 error envelope = %+v", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, postgresAPIDSNEnv) {
		t.Fatalf("disabled message does not name the explicit configuration: %q", envelope.Error.Message)
	}

	legacyResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyResponse, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if legacyResponse.Code != http.StatusOK || legacyCalls != 1 || legacyResponse.Body.String() != "legacy sqlite handler" {
		t.Fatalf("legacy response status=%d calls=%d body=%q", legacyResponse.Code, legacyCalls, legacyResponse.Body.String())
	}
}

func TestExplicitInvalidPostgresDSNFailsInsteadOfFallingBack(t *testing.T) {
	legacyCalls := 0
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		legacyCalls++
		w.WriteHeader(http.StatusOK)
	})
	handler, closeDB, err := configureAPIRoutes(legacy, "operator-token", "not a PostgreSQL DSN")
	if closeDB != nil {
		closeDB()
	}
	if err == nil || handler != nil {
		t.Fatalf("invalid explicit DSN returned handler=%v err=%v", handler, err)
	}
	if legacyCalls != 0 {
		t.Fatalf("invalid explicit DSN reached legacy handler %d times", legacyCalls)
	}
}

// TestPostgresAPIRouteMountIntegration requires a disposable database named
// porter_test_api_mount_*. It proves the server rejects an un-migrated schema,
// does not auto-apply migrations, then mounts PostgreSQL `/api/v1` beside the
// legacy `/v1` handler after an explicit migration run.
func TestPostgresAPIRouteMountIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_API_MOUNT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_API_MOUNT_TEST_POSTGRES_DSN to a disposable porter_test_api_mount_* database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to disposable PostgreSQL database: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_api_mount_") {
		t.Fatalf("refusing destructive test in database %q; name must start with porter_test_api_mount_", databaseName)
	}
	if err := resetAPIMountTestDB(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetAPIMountTestDB(cleanupCtx, db); err != nil {
			t.Errorf("clean up disposable test database: %v", err)
		}
	})

	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("legacy sqlite handler"))
	})
	if handler, closeDB, err := configureAPIRoutes(legacy, "operator-token", dsn); err == nil {
		closeDB()
		t.Fatalf("un-migrated PostgreSQL database unexpectedly mounted: %v", handler)
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('porter_control_plane.projects') IS NOT NULL`).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatal("server configuration automatically applied PostgreSQL migrations")
	}

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply schema explicitly: %v", err)
	}
	handler, closeDB, err := configureAPIRoutes(legacy, "operator-token", dsn)
	if err != nil {
		t.Fatalf("mount migrated PostgreSQL API: %v", err)
	}
	defer closeDB()

	v1Response := httptest.NewRecorder()
	v1Request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	v1Request.Header.Set("Authorization", "Bearer operator-token")
	handler.ServeHTTP(v1Response, v1Request)
	if v1Response.Code != http.StatusOK {
		t.Fatalf("PostgreSQL v1 response status=%d body=%s", v1Response.Code, v1Response.Body.String())
	}
	var page v1.Page[v1.Project]
	if err := json.Unmarshal(v1Response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("fresh PostgreSQL projects page = %+v", page)
	}

	legacyResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyResponse, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if legacyResponse.Code != http.StatusOK || legacyResponse.Body.String() != "legacy sqlite handler" {
		t.Fatalf("legacy `/v1` response status=%d body=%q", legacyResponse.Code, legacyResponse.Body.String())
	}
}

func resetAPIMountTestDB(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`DROP SCHEMA IF EXISTS porter_control_plane CASCADE`,
		`DROP TABLE IF EXISTS public.porter_control_plane_schema_migrations`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}
