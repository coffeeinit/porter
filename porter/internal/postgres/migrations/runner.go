package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const (
	advisoryLockID int64 = 0x505254525f4d4947 // "PRTR_MIG", stable across runner processes.
	ledgerTable          = "public.porter_control_plane_schema_migrations"
)

var (
	ErrChecksumDrift    = errors.New("PostgreSQL migration checksum drift")
	ErrMigrationHistory = errors.New("incompatible PostgreSQL migration history")

	migrationFilename = regexp.MustCompile(`^([0-9]{4})_[a-z0-9][a-z0-9_]*\.sql$`)
)

type migration struct {
	version  int64
	name     string
	body     []byte
	checksum string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

// Apply applies embedded PostgreSQL migrations. It is an explicit operation;
// the active control-plane server does not call it during startup.
func Apply(ctx context.Context, db *sql.DB) error {
	return applyFrom(ctx, db, FS)
}

// applyFrom is injectable for regression tests that simulate file drift and
// failed/retried future migrations. Production callers use Apply and FS.
func applyFrom(ctx context.Context, db *sql.DB, source fs.FS) (err error) {
	if db == nil {
		return errors.New("PostgreSQL migration database is nil")
	}
	files, err := loadMigrations(source)
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get PostgreSQL migration connection: %w", err)
	}
	defer conn.Close()

	lockAttempted := false
	lockAcquired := false
	defer func() {
		if !lockAttempted {
			return
		}
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if unlockErr := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, advisoryLockID).Scan(&unlocked); unlockErr != nil {
			cleanupErr := fmt.Errorf("release PostgreSQL migration advisory lock; discard uncertain connection: %w", unlockErr)
			if discardErr := discardConnection(conn); discardErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("discard migration connection: %w", discardErr))
			}
			err = errors.Join(err, cleanupErr)
			return
		}
		if lockAcquired && !unlocked {
			cleanupErr := errors.New("PostgreSQL migration advisory lock was not held at release; discard uncertain connection")
			if discardErr := discardConnection(conn); discardErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("discard migration connection: %w", discardErr))
			}
			err = errors.Join(err, cleanupErr)
		}
	}()

	// Treat an acquisition error as an uncertain lock state: cancellation may
	// arrive after PostgreSQL took the session lock but before the response.
	lockAttempted = true
	if _, lockErr := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); lockErr != nil {
		return fmt.Errorf("acquire PostgreSQL migration advisory lock: %w", lockErr)
	}
	lockAcquired = true

	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`, ledgerTable)); err != nil {
		return fmt.Errorf("ensure PostgreSQL migration ledger: %w", err)
	}

	applied, err := readApplied(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateApplied(applied, files); err != nil {
		return err
	}

	for _, file := range files[len(applied):] {
		if err := applyOne(ctx, conn, file); err != nil {
			return err
		}
	}
	return nil
}

func discardConnection(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if err != nil && !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		return err
	}
	return nil
}

func loadMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded PostgreSQL migrations: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("no PostgreSQL migrations found")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	files := make([]migration, 0, len(entries))
	expectedVersion := int64(InitialVersion)
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("unexpected directory in PostgreSQL migrations: %s", entry.Name())
		}
		match := migrationFilename.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid PostgreSQL migration filename %q; want NNNN_name.sql", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version in %q: %w", entry.Name(), err)
		}
		if version != expectedVersion {
			return nil, fmt.Errorf("PostgreSQL migration sequence gap: got version %04d, want %04d", version, expectedVersion)
		}
		body, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read PostgreSQL migration %s: %w", entry.Name(), err)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("PostgreSQL migration %s is empty", entry.Name())
		}
		digest := sha256.Sum256(body)
		files = append(files, migration{
			version:  version,
			name:     entry.Name(),
			body:     body,
			checksum: hex.EncodeToString(digest[:]),
		})
		expectedVersion++
	}
	return files, nil
}

func readApplied(ctx context.Context, conn *sql.Conn) ([]appliedMigration, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`
SELECT version, name, checksum
FROM %s
ORDER BY version`, ledgerTable))
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL migration ledger: %w", err)
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL migration ledger: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL migration ledger: %w", err)
	}
	return applied, nil
}

func validateApplied(applied []appliedMigration, available []migration) error {
	if len(applied) > len(available) {
		return fmt.Errorf("%w: database has %d applied versions but this binary contains %d", ErrMigrationHistory, len(applied), len(available))
	}
	for index, recorded := range applied {
		expected := available[index]
		if recorded.version != expected.version {
			return fmt.Errorf("%w: recorded version %04d where contiguous version %04d was expected", ErrMigrationHistory, recorded.version, expected.version)
		}
		if recorded.name != expected.name || recorded.checksum != expected.checksum {
			return fmt.Errorf("%w: version %04d (%s), recorded name/checksum %q/%s, embedded %q/%s", ErrChecksumDrift, expected.version, expected.name, recorded.name, recorded.checksum, expected.name, expected.checksum)
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, file migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL migration %04d (%s): %w", file.version, file.name, err)
	}
	if _, err := tx.ExecContext(ctx, string(file.body)); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("apply PostgreSQL migration %04d (%s) failed and rollback failed (%v): %w", file.version, file.name, rollbackErr, err)
		}
		return fmt.Errorf("apply PostgreSQL migration %04d (%s); transaction rolled back, safe to retry: %w", file.version, file.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (version, name, checksum)
VALUES ($1, $2, $3)`, ledgerTable), file.version, file.name, file.checksum); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("record PostgreSQL migration %04d (%s) failed and rollback failed (%v): %w", file.version, file.name, rollbackErr, err)
		}
		return fmt.Errorf("record PostgreSQL migration %04d (%s); transaction rolled back, safe to retry: %w", file.version, file.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit PostgreSQL migration %04d (%s); rerun Apply to reconcile from the ledger: %w", file.version, file.name, err)
	}
	return nil
}
