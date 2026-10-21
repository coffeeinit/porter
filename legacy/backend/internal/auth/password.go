// Package auth contains shared credential primitives for database-backed
// Porter authentication. It deliberately does not know about HTTP, roles, or
// configuration fallbacks.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// NewSalt returns a cryptographically random salt suitable for a persisted
// password row.
func NewSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HashPassword is the compatibility hash used by the current schema. The
// salt is persisted beside the hash; no plaintext password is stored.
func HashPassword(password, salt string) string {
	h := sha256.Sum256([]byte(salt + password))
	return hex.EncodeToString(h[:])
}

func VerifyPassword(password, salt, expected string) bool {
	if password == "" || salt == "" || expected == "" {
		return false
	}
	got := HashPassword(password, salt)
	if len(got) != len(expected) {
		return false
	}
	var diff byte
	for i := range got {
		diff |= got[i] ^ expected[i]
	}
	return diff == 0
}

// ValidateBootstrapPassword prevents accidentally bootstrapping an empty
// credential from an unset environment variable.
func ValidateBootstrapPassword(password string) error {
	if len(password) < 12 {
		return errors.New("bootstrap admin password must be at least 12 characters")
	}
	return nil
}
