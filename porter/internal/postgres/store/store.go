// Package store provides persistence methods for the proposed v1 PostgreSQL
// resource model. It does not open a database, apply migrations, or replace the
// active SQLite store.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidArgument            = errors.New("invalid argument")
	ErrNotFound                   = errors.New("resource not found")
	ErrFailedPrecondition         = errors.New("failed precondition")
	ErrEnrollmentTokenUnavailable = errors.New("enrollment token unavailable")
)

type Store struct {
	db *sql.DB
}

// New wraps an already-configured PostgreSQL database handle. It neither owns
// nor closes the handle, and it never applies schema migrations.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL database handle is required")
	}
	return &Store{db: db}, nil
}

func newID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate resource ID: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

func validateName(kind, name string) error {
	if name == "" || utf8.RuneCountInString(name) > 128 {
		return fmt.Errorf("%w: %s name must contain 1 to 128 characters", ErrInvalidArgument, kind)
	}
	return nil
}

func notFound(kind, id string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %q", ErrNotFound, kind, id)
	}
	return err
}

func requireOneRow(result sql.Result, kind, id string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s update count: %w", kind, err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %s %q", ErrNotFound, kind, id)
	}
	return nil
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validCommit(value string) bool {
	return (len(value) == 40 || len(value) == 64) && isLowerHex(value)
}

func validArtifactDigest(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+64 && isLowerHex(value[len(prefix):])
}
