package systemdcred

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPostgresDSNFromSystemdCredential(t *testing.T) {
	directory := t.TempDir()
	want := "postgresql://porter@127.0.0.1/porter?sslmode=disable"
	if err := os.WriteFile(filepath.Join(directory, PostgresDSNCredential), []byte("  "+want+"\n"), 0o600); err != nil {
		t.Fatal("write credential fixture")
	}
	got, err := ReadPostgresDSN(func(key string) string {
		if key == CredentialsDirectoryEnv {
			return directory
		}
		return ""
	})
	if err != nil || got != want {
		t.Fatal("systemd credential did not load as expected")
	}
}

func TestReadPostgresDSNRequiresSystemdCredentialDirectory(t *testing.T) {
	secret := "postgresql://porter:must-not-be-read-from-env@localhost/porter"
	_, err := ReadPostgresDSN(func(key string) string {
		if key == "PORTER_POSTGRES_DSN" {
			return secret
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "systemd >= 247") || strings.Contains(err.Error(), secret) {
		t.Fatal("missing systemd credential did not fail closed with a generic systemd-version error")
	}
}

func TestReadPostgresDSNRejectsMissingOrMalformedCredential(t *testing.T) {
	directory := t.TempDir()
	if _, err := ReadPostgresDSN(func(key string) string {
		if key == CredentialsDirectoryEnv {
			return directory
		}
		return ""
	}); err == nil {
		t.Fatal("missing credential file unexpectedly loaded")
	}

	secret := "postgresql://porter:do-not-print@localhost/porter"
	path := filepath.Join(directory, PostgresDSNCredential)
	if err := os.WriteFile(path, []byte(secret+"\nsecond-line"), 0o600); err != nil {
		t.Fatal("write malformed credential fixture")
	}
	_, err := ReadPostgresDSN(func(key string) string {
		if key == CredentialsDirectoryEnv {
			return directory
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "do-not-print") {
		t.Fatal("malformed credential did not fail with a redacted error")
	}
}

func TestReadAdminTokenFromSystemdCredential(t *testing.T) {
	directory := t.TempDir()
	want := "operator-secret-never-env"
	if err := os.WriteFile(filepath.Join(directory, AdminTokenCredential), []byte(want), 0o600); err != nil {
		t.Fatal("write admin credential fixture")
	}
	got, err := ReadAdminToken(func(key string) string {
		if key == CredentialsDirectoryEnv {
			return directory
		}
		return ""
	})
	if err != nil || got != want {
		t.Fatal("operator credential did not load as expected")
	}

	_, err = ReadAdminToken(func(key string) string {
		if key == "PORTER_ADMIN_TOKEN" {
			return want
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), want) {
		t.Fatal("operator credential unexpectedly loaded from an environment variable")
	}
}
