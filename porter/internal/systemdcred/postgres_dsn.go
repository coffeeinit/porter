package systemdcred

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	CredentialsDirectoryEnv = "CREDENTIALS_DIRECTORY"
	PostgresDSNCredential   = "porter-postgres-dsn"
	AdminTokenCredential    = "porter-admin-token"
	maxCredentialBytes      = 16 * 1024
)

var (
	ErrCredentialsUnavailable = errors.New("systemd credentials unavailable; systemd >= 247 with LoadCredential is required")
	ErrCredentialInvalid      = errors.New("systemd credential is unreadable or invalid")
)

// ReadPostgresDSN reads the DSN only from systemd's per-unit credential
// directory. It intentionally has no environment-variable or argv fallback.
func ReadPostgresDSN(getenv func(string) string) (string, error) {
	return ReadCredential(PostgresDSNCredential, getenv)
}

// ReadAdminToken reads the operator Bearer only from systemd's per-unit
// credential directory. It intentionally has no environment-variable or argv fallback.
func ReadAdminToken(getenv func(string) string) (string, error) {
	return ReadCredential(AdminTokenCredential, getenv)
}

// ReadCredential reads one named secret from CREDENTIALS_DIRECTORY and returns
// only generic errors. The directory is supplied by systemd LoadCredential.
func ReadCredential(name string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	directory := strings.TrimSpace(getenv(CredentialsDirectoryEnv))
	if directory == "" || !filepath.IsAbs(directory) || filepath.Base(name) != name || name == "." || name == ".." {
		return "", ErrCredentialsUnavailable
	}

	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrCredentialInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrCredentialInvalid
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(contents) > maxCredentialBytes {
		return "", ErrCredentialInvalid
	}
	secret := strings.TrimSpace(string(contents))
	if secret == "" || strings.ContainsAny(secret, "\x00\r\n") {
		return "", ErrCredentialInvalid
	}
	return secret, nil
}
