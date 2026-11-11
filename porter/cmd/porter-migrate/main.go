package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"porter/controlplane/internal/postgres/migrations"
	"porter/controlplane/internal/systemdcred"
)

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	if len(args) != 1 || args[0] != "apply" {
		return errors.New("usage: porter-migrate apply")
	}
	dsn, err := systemdcred.ReadPostgresDSN(getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("open PostgreSQL migration connection failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	pingContext, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	defer cancelPing()
	if err := db.PingContext(pingContext); err != nil {
		return errors.New("connect to PostgreSQL migration database failed")
	}
	if err := migrations.Apply(ctx, db); err != nil {
		return errors.New("apply PostgreSQL migrations failed")
	}
	return nil
}
