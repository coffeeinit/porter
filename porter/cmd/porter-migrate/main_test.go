package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"porter/controlplane/internal/systemdcred"
)

func TestRunRequiresExplicitApplySubcommand(t *testing.T) {
	err := run(nil, func(string) string { return "unused" })
	if err == nil || !strings.Contains(err.Error(), "porter-migrate apply") {
		t.Fatal("migration CLI did not require the explicit apply subcommand")
	}
}

func TestRunRequiresSystemdCredential(t *testing.T) {
	err := run([]string{"apply"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "systemd >= 247") || strings.Contains(err.Error(), "PORTER_POSTGRES_DSN") {
		t.Fatal("migration CLI did not fail closed on a missing systemd credential")
	}
}

func TestRunRedactsDSNFromConnectionFailure(t *testing.T) {
	directory := t.TempDir()
	dsn := "postgresql://porter:do-not-print@127.0.0.1:1/porter?sslmode=disable"
	if err := os.WriteFile(filepath.Join(directory, systemdcred.PostgresDSNCredential), []byte(dsn), 0o600); err != nil {
		t.Fatal("write DSN credential fixture")
	}
	err := run([]string{"apply"}, func(key string) string {
		if key == systemdcred.CredentialsDirectoryEnv {
			return directory
		}
		return ""
	})
	if err == nil || strings.Contains(err.Error(), dsn) || strings.Contains(err.Error(), "do-not-print") {
		t.Fatal("migration connection failure did not return a generic redacted error")
	}
}
