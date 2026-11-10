package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"
	"porter/controlplane/internal/postgres/migrations"
)

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	if len(args) != 1 || args[0] != "apply" {
		return fmt.Errorf("usage: porter-migrate apply")
	}
	dsn := strings.TrimSpace(getenv("PORTER_POSTGRES_DSN"))
	if dsn == "" {
		return fmt.Errorf("PORTER_POSTGRES_DSN is required for the explicit apply command")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open PostgreSQL migration connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := migrations.Apply(ctx, db); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	return nil
}
