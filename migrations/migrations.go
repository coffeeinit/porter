// Package migrations embeds the SQL migration files so the Go binary can run
// them at startup without depending on the filesystem layout at runtime.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
