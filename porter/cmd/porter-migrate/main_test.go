package main

import (
	"strings"
	"testing"
)

func TestRunRequiresExplicitApplySubcommand(t *testing.T) {
	err := run(nil, func(string) string { return "postgres://unused" })
	if err == nil || !strings.Contains(err.Error(), "porter-migrate apply") {
		t.Fatalf("run error = %v, want explicit apply usage", err)
	}
}

func TestRunRequiresPostgresDSN(t *testing.T) {
	err := run([]string{"apply"}, func(string) string { return " " })
	if err == nil || !strings.Contains(err.Error(), "PORTER_POSTGRES_DSN is required") {
		t.Fatalf("run error = %v, want explicit PostgreSQL DSN requirement", err)
	}
}
