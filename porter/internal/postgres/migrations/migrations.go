// Package migrations embeds and explicitly applies PostgreSQL schema migrations.
// It is not imported or invoked by the active control plane server.
package migrations

import "embed"

const InitialVersion = 1

// FS contains the forward-only PostgreSQL migration files.
//
//go:embed *.sql
var FS embed.FS
