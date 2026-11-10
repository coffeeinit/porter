package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"porter/controlplane/internal/postgres/httpapi"
	pgstore "porter/controlplane/internal/postgres/store"
)

const postgresAPIDSNEnv = "PORTER_POSTGRES_API_DSN"

// configureAPIRoutes mounts the PostgreSQL-backed v1 adapter only when its DSN
// is explicitly supplied. The legacy handler remains the fallback for other
// paths, but `/api/v1` is always handled by the PostgreSQL adapter or a 503.
// Migrations are never applied here.
func configureAPIRoutes(legacy http.Handler, adminToken, dsn string) (http.Handler, func(), error) {
	if legacy == nil {
		return nil, nil, fmt.Errorf("legacy API handler is required")
	}

	v1Handler := httpapi.UnavailableHandler()
	closePostgres := func() {}
	dsn = strings.TrimSpace(dsn)
	if dsn != "" {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("open PostgreSQL API database: %w", err)
		}
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("connect to PostgreSQL API database: %w", err)
		}
		if err := verifyPostgresAPISchema(ctx, db); err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		repository, err := pgstore.New(db)
		if err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("initialize PostgreSQL API store: %w", err)
		}
		adapter, err := httpapi.New(repository, adminToken)
		if err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("initialize PostgreSQL v1 API: %w", err)
		}
		v1Handler = adapter
		closePostgres = func() { _ = db.Close() }
	}

	mux := http.NewServeMux()
	mux.Handle("/api/v1", v1Handler)
	mux.Handle("/api/v1/", v1Handler)
	mux.Handle("/", legacy)
	return mux, closePostgres, nil
}

func verifyPostgresAPISchema(ctx context.Context, db *sql.DB) error {
	var ready bool
	err := db.QueryRowContext(ctx, `
	SELECT to_regclass('public.porter_control_plane_schema_migrations') IS NOT NULL
	   AND to_regclass('porter_control_plane.projects') IS NOT NULL
	   AND to_regclass('porter_control_plane.services') IS NOT NULL
	   AND to_regclass('porter_control_plane.releases') IS NOT NULL
	   AND to_regclass('porter_control_plane.instances') IS NOT NULL
	   AND to_regclass('porter_control_plane.fleet_nodes') IS NOT NULL
	   AND to_regclass('porter_control_plane.resource_events') IS NOT NULL
	   AND to_regclass('porter_control_plane.domain_claims') IS NOT NULL
	   AND to_regclass('porter_control_plane.gateway_routes') IS NOT NULL
	   AND to_regclass('porter_control_plane.volume_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.snapshot_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.backup_metadata') IS NOT NULL
	   AND to_regclass('porter_control_plane.operations') IS NOT NULL
		   AND to_regclass('porter_control_plane.agent_commands') IS NOT NULL
		   AND to_regclass('porter_control_plane.agent_command_results') IS NOT NULL
		   AND to_regclass('porter_control_plane.agent_enrollment_tokens') IS NOT NULL
		   AND to_regclass('porter_control_plane.telemetry_logs') IS NOT NULL
		   AND to_regclass('porter_control_plane.telemetry_metrics') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("verify PostgreSQL API schema: %w", err)
	}
	if !ready {
		return fmt.Errorf("PostgreSQL API schema is not initialized; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var operationsMigrationApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
		    WHERE version = 5 AND name = '0005_operations_command_leases.sql'
	)`).Scan(&operationsMigrationApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL API migration history: %w", err)
	}
	if !operationsMigrationApplied {
		return fmt.Errorf("PostgreSQL API requires migration 0005_operations_command_leases.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var enrollmentMigrationApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 7 AND name = '0007_agent_enrollment.sql'
)`).Scan(&enrollmentMigrationApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL API migration history: %w", err)
	}
	if !enrollmentMigrationApplied {
		return fmt.Errorf("PostgreSQL API requires migration 0007_agent_enrollment.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var snapshotMigrationApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 8 AND name = '0008_snapshot_restore_lifecycle.sql'
)`).Scan(&snapshotMigrationApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL API migration history: %w", err)
	}
	if !snapshotMigrationApplied {
		return fmt.Errorf("PostgreSQL API requires migration 0008_snapshot_restore_lifecycle.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var releaseReadinessMigrationApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 9 AND name = '0009_release_readiness_health_gate.sql'
)`).Scan(&releaseReadinessMigrationApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL API migration history: %w", err)
	}
	if !releaseReadinessMigrationApplied {
		return fmt.Errorf("PostgreSQL API requires migration 0009_release_readiness_health_gate.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var telemetryMigrationApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 10 AND name = '0010_agent_telemetry.sql'
)`).Scan(&telemetryMigrationApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL API migration history: %w", err)
	}
	if !telemetryMigrationApplied {
		return fmt.Errorf("PostgreSQL API requires migration 0010_agent_telemetry.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	var gitOpsMigrationsApplied bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 11 AND name = '0011_gitops_release_intent.sql'
) AND EXISTS (
    SELECT 1 FROM public.porter_control_plane_schema_migrations
    WHERE version = 12 AND name = '0012_release_builder_receipts.sql'
)`).Scan(&gitOpsMigrationsApplied); err != nil {
		return fmt.Errorf("verify PostgreSQL GitOps migration history: %w", err)
	}
	if !gitOpsMigrationsApplied {
		return fmt.Errorf("PostgreSQL GitOps API requires migrations 0011_gitops_release_intent.sql and 0012_release_builder_receipts.sql; run `go run ./cmd/porter-migrate apply` with PORTER_POSTGRES_DSN first")
	}
	return nil
}
