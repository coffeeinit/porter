package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresRunnerIntegration is destructive only inside a database whose
// name starts with porter_test_. Set PORTER_TEST_POSTGRES_DSN to an explicitly
// disposable PostgreSQL database to enable it.
func TestPostgresRunnerIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_TEST_POSTGRES_DSN to a disposable porter_test_* PostgreSQL database")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_") {
		t.Fatalf("refusing destructive integration test in database %q; name must start with porter_test_", databaseName)
	}

	if err := resetIntegrationObjects(ctx, db); err != nil {
		t.Fatalf("reset disposable integration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetIntegrationObjects(cleanupCtx, db); err != nil {
			t.Errorf("clean up disposable integration database: %v", err)
		}
	})

	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		_ = blocker.Close()
		t.Fatalf("hold migration advisory lock for cancellation test: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 75*time.Millisecond)
	waitErr := Apply(waitCtx, db)
	waitCancel()
	if waitErr == nil || !strings.Contains(waitErr.Error(), "acquire PostgreSQL migration advisory lock") {
		_, _ = blocker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockID)
		_ = blocker.Close()
		t.Fatalf("Apply while another session holds the lock = %v, want a canceled lock wait", waitErr)
	}
	var blockerUnlocked bool
	if err := blocker.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockID).Scan(&blockerUnlocked); err != nil {
		_ = blocker.Close()
		t.Fatalf("release lock after canceled Apply: %v", err)
	}
	if !blockerUnlocked {
		_ = blocker.Close()
		t.Fatal("test lock was not held after canceled Apply")
	}
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}

	// Two independent callers must serialize on the same PostgreSQL advisory lock.
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- Apply(ctx, db)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent fresh Apply: %v", err)
		}
	}

	assertLedgerCount(t, ctx, db, 12)
	assertCoreTableCount(t, ctx, db, 20)
	var firstAppliedAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT applied_at FROM public.porter_control_plane_schema_migrations WHERE version = 1`).Scan(&firstAppliedAt); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("repeat Apply: %v", err)
	}
	assertLedgerCount(t, ctx, db, 12)
	var secondAppliedAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT applied_at FROM public.porter_control_plane_schema_migrations WHERE version = 1`).Scan(&secondAppliedAt); err != nil {
		t.Fatal(err)
	}
	if !firstAppliedAt.Equal(secondAppliedAt) {
		t.Fatalf("repeat Apply changed applied_at from %s to %s", firstAppliedAt, secondAppliedAt)
	}

	original, err := FS.ReadFile("0001_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	eventsMigration, err := FS.ReadFile("0002_resource_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	domainsMigration, err := FS.ReadFile("0003_domains_routes.sql")
	if err != nil {
		t.Fatal(err)
	}
	storageMigration, err := FS.ReadFile("0004_storage_backup_metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	operationsMigration, err := FS.ReadFile("0005_operations_command_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	credentialsMigration, err := FS.ReadFile("0006_agent_node_credentials.sql")
	if err != nil {
		t.Fatal(err)
	}
	enrollmentMigration, err := FS.ReadFile("0007_agent_enrollment.sql")
	if err != nil {
		t.Fatal(err)
	}
	snapshotMigration, err := FS.ReadFile("0008_snapshot_restore_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	readinessMigration, err := FS.ReadFile("0009_release_readiness_health_gate.sql")
	if err != nil {
		t.Fatal(err)
	}
	telemetryMigration, err := FS.ReadFile("0010_agent_telemetry.sql")
	if err != nil {
		t.Fatal(err)
	}
	gitopsMigration, err := FS.ReadFile("0011_gitops_release_intent.sql")
	if err != nil {
		t.Fatal(err)
	}
	builderReceiptsMigration, err := FS.ReadFile("0012_release_builder_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	drifted := append(append([]byte(nil), eventsMigration...), []byte("\n-- simulated migration drift\n")...)
	driftFS := fstest.MapFS{
		"0001_core.sql":                          &fstest.MapFile{Data: original},
		"0002_resource_events.sql":               &fstest.MapFile{Data: drifted},
		"0003_domains_routes.sql":                &fstest.MapFile{Data: domainsMigration},
		"0004_storage_backup_metadata.sql":       &fstest.MapFile{Data: storageMigration},
		"0005_operations_command_leases.sql":     &fstest.MapFile{Data: operationsMigration},
		"0006_agent_node_credentials.sql":        &fstest.MapFile{Data: credentialsMigration},
		"0007_agent_enrollment.sql":              &fstest.MapFile{Data: enrollmentMigration},
		"0008_snapshot_restore_lifecycle.sql":    &fstest.MapFile{Data: snapshotMigration},
		"0009_release_readiness_health_gate.sql": &fstest.MapFile{Data: readinessMigration},
		"0010_agent_telemetry.sql":               &fstest.MapFile{Data: telemetryMigration},
		"0011_gitops_release_intent.sql":         &fstest.MapFile{Data: gitopsMigration},
		"0012_release_builder_receipts.sql":      &fstest.MapFile{Data: builderReceiptsMigration},
	}
	if err := applyFrom(ctx, db, driftFS); !errors.Is(err, ErrChecksumDrift) {
		t.Fatalf("drifted migration error = %v, want ErrChecksumDrift", err)
	}

	// A failed migration and its ledger insert share one transaction; a corrected
	// migration with the same version can then be retried safely.
	badFS := fstest.MapFS{
		"0001_core.sql":                          &fstest.MapFile{Data: original},
		"0002_resource_events.sql":               &fstest.MapFile{Data: eventsMigration},
		"0003_domains_routes.sql":                &fstest.MapFile{Data: domainsMigration},
		"0004_storage_backup_metadata.sql":       &fstest.MapFile{Data: storageMigration},
		"0005_operations_command_leases.sql":     &fstest.MapFile{Data: operationsMigration},
		"0006_agent_node_credentials.sql":        &fstest.MapFile{Data: credentialsMigration},
		"0007_agent_enrollment.sql":              &fstest.MapFile{Data: enrollmentMigration},
		"0008_snapshot_restore_lifecycle.sql":    &fstest.MapFile{Data: snapshotMigration},
		"0009_release_readiness_health_gate.sql": &fstest.MapFile{Data: readinessMigration},
		"0010_agent_telemetry.sql":               &fstest.MapFile{Data: telemetryMigration},
		"0011_gitops_release_intent.sql":         &fstest.MapFile{Data: gitopsMigration},
		"0012_release_builder_receipts.sql":      &fstest.MapFile{Data: builderReceiptsMigration},
		"0013_retry_probe.sql":                   &fstest.MapFile{Data: []byte("CREATE TABLE porter_control_plane.porter_migration_retry_probe (id INTEGER);\nTHIS IS INVALID SQL;\n")},
	}
	if err := applyFrom(ctx, db, badFS); err == nil {
		t.Fatal("invalid version 13 unexpectedly applied")
	} else if !strings.Contains(err.Error(), "transaction rolled back, safe to retry") {
		t.Fatalf("failed migration error should state rollback/retry behavior, got %v", err)
	}
	assertLedgerCount(t, ctx, db, 12)
	assertRetryProbeAbsent(t, ctx, db)

	retryFS := fstest.MapFS{
		"0001_core.sql":                          &fstest.MapFile{Data: original},
		"0002_resource_events.sql":               &fstest.MapFile{Data: eventsMigration},
		"0003_domains_routes.sql":                &fstest.MapFile{Data: domainsMigration},
		"0004_storage_backup_metadata.sql":       &fstest.MapFile{Data: storageMigration},
		"0005_operations_command_leases.sql":     &fstest.MapFile{Data: operationsMigration},
		"0006_agent_node_credentials.sql":        &fstest.MapFile{Data: credentialsMigration},
		"0007_agent_enrollment.sql":              &fstest.MapFile{Data: enrollmentMigration},
		"0008_snapshot_restore_lifecycle.sql":    &fstest.MapFile{Data: snapshotMigration},
		"0009_release_readiness_health_gate.sql": &fstest.MapFile{Data: readinessMigration},
		"0010_agent_telemetry.sql":               &fstest.MapFile{Data: telemetryMigration},
		"0011_gitops_release_intent.sql":         &fstest.MapFile{Data: gitopsMigration},
		"0012_release_builder_receipts.sql":      &fstest.MapFile{Data: builderReceiptsMigration},
		"0013_retry_probe.sql":                   &fstest.MapFile{Data: []byte("CREATE TABLE porter_control_plane.porter_migration_retry_probe (id INTEGER);\n")},
	}
	if err := applyFrom(ctx, db, retryFS); err != nil {
		t.Fatalf("retry corrected version 13: %v", err)
	}
	assertLedgerCount(t, ctx, db, 13)
	var probeExists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('porter_control_plane.porter_migration_retry_probe') IS NOT NULL`).Scan(&probeExists); err != nil {
		t.Fatal(err)
	}
	if !probeExists {
		t.Fatal("successful retry did not create the probe table")
	}
}

func resetIntegrationObjects(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`DROP SCHEMA IF EXISTS porter_control_plane CASCADE`,
		`DROP TABLE IF EXISTS public.porter_control_plane_schema_migrations`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func assertLedgerCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.porter_control_plane_schema_migrations`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("migration ledger count = %d, want %d", got, want)
	}
}

func assertCoreTableCount(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'porter_control_plane' AND table_type = 'BASE TABLE'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("core table count = %d, want %d", got, want)
	}
}

func assertRetryProbeAbsent(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('porter_control_plane.porter_migration_retry_probe') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed migration left its transactional DDL behind")
	}
}
