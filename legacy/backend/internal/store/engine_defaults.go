// Engine defaults (migration 0035): per-engine superuser + default
// healthcheck probe, previously hardcoded in marketplace.defaultServiceUser.
// The DB is the source of truth; the Go map in marketplace remains as
// fallback only for when the DB is unreachable or a row is missing.
package store

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a row lookup has no match. Callers fall back
// to built-in defaults; the store itself never falls back.
var ErrNotFound = errors.New("store: not found")

// EngineDefault is one row of engine_defaults: the conventional superuser
// plus the default healthcheck probe for one-click service templates.
type EngineDefault struct {
	Engine     string // postgres, mysql, mariadb, mongo, redis, minio
	Superuser  string // conventional superuser (postgres, root, default, porter)
	HCType     string // tcp | http
	HCPort     int    // probe port (minio: 9000; console 9001 is publish-only)
	HCInterval int    // probe interval seconds
}

// GetEngineDefault returns the row for engine (case-insensitive) or
// ErrNotFound when the engine has no row. No fallback inside the store —
// the caller (marketplace.ResolveTemplate) decides what to do next.
func (s *Store) GetEngineDefault(engine string) (EngineDefault, error) {
	var d EngineDefault
	err := s.pool.QueryRow(context.Background(),
		`SELECT engine, superuser, hc_type, hc_port, hc_interval
		 FROM engine_defaults WHERE lower(engine) = lower($1)`,
		strings.TrimSpace(engine)).Scan(
		&d.Engine, &d.Superuser, &d.HCType, &d.HCPort, &d.HCInterval)
	if errors.Is(err, pgx.ErrNoRows) {
		return EngineDefault{}, ErrNotFound
	}
	if err != nil {
		return EngineDefault{}, err
	}
	return d, nil
}

// ListEngineDefaults returns all rows ordered by engine. A nil/empty result
// means the DB is unreachable or unseeded — callers treat that as
// "use built-ins", never as an error to surface to operators.
func (s *Store) ListEngineDefaults() []EngineDefault {
	rows, err := s.pool.Query(context.Background(),
		`SELECT engine, superuser, hc_type, hc_port, hc_interval
		 FROM engine_defaults ORDER BY engine`)
	if err != nil {
		log.Printf("store: list engine defaults: %v", err)
		return nil
	}
	defer rows.Close()
	var out []EngineDefault
	for rows.Next() {
		var d EngineDefault
		if err := rows.Scan(&d.Engine, &d.Superuser, &d.HCType, &d.HCPort, &d.HCInterval); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out
}
