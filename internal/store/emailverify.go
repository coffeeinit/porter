// This file implements email verification state (migration 0037): a
// single-use token ledger keyed by SHA-256 hash. The plain token is shown to
// the caller once and never persisted, so a database dump cannot be replayed
// as a verification.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// emailVerificationTTL bounds how long a minted token stays consumable.
const emailVerificationTTL = 24 * time.Hour

// Typed consumption failures: the API maps each to an explicit message
// instead of one opaque "bad token".
var (
	ErrEmailVerificationUnknown = errors.New("email verification token not recognized")
	ErrEmailVerificationUsed    = errors.New("email verification token already used")
	ErrEmailVerificationExpired = errors.New("email verification token expired")
)

// CreateEmailVerification mints a single-use verification token for userID
// and returns the plain token exactly once. Only its SHA-256 hash is stored.
func (s *Store) CreateEmailVerification(userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint verification token: %w", err)
	}
	plain := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO email_verifications (user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`,
		userID, hex.EncodeToString(sum[:]), time.Now().Add(emailVerificationTTL)); err != nil {
		return "", fmt.Errorf("store verification token: %w", err)
	}
	return plain, nil
}

// ConsumeEmailVerification validates the plain token (exists, unexpired,
// unused), flips used_at, and returns the owning user's ID. The UPDATE with
// `used_at IS NULL` guard makes concurrent double-spend impossible: exactly
// one caller wins the row flip.
func (s *Store) ConsumeEmailVerification(plainToken string) (string, error) {
	if plainToken == "" {
		return "", ErrEmailVerificationUnknown
	}
	sum := sha256.Sum256([]byte(plainToken))
	hash := hex.EncodeToString(sum[:])

	var userID string
	var expiresAt time.Time
	var usedAt *time.Time
	err := s.pool.QueryRow(context.Background(),
		`SELECT user_id, expires_at, used_at FROM email_verifications WHERE token_hash = $1`,
		hash).Scan(&userID, &expiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrEmailVerificationUnknown
	}
	if err != nil {
		return "", fmt.Errorf("load verification token: %w", err)
	}
	if usedAt != nil {
		return "", ErrEmailVerificationUsed
	}
	if time.Now().After(expiresAt) {
		return "", ErrEmailVerificationExpired
	}
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE email_verifications SET used_at = now() WHERE token_hash = $1 AND used_at IS NULL`,
		hash)
	if err != nil {
		return "", fmt.Errorf("consume verification token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrEmailVerificationUsed
	}
	return userID, nil
}

// MarkUserEmailVerified flips the user's email_verified flag (migration 0037).
func (s *Store) MarkUserEmailVerified(userID string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE users SET email_verified = TRUE WHERE id::text = $1`, userID)
	if err != nil {
		log.Printf("store: mark user %s email verified: %v", userID, err)
	}
	return err
}
