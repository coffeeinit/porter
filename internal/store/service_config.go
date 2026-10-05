// Durable service config truth (OCM-15 remainder): one row per service
// carrying the optimistic-concurrency revision plus the env map. The
// config-push applier compare-and-swaps on base_rev; guest boot renders
// the managed env block from the winning row.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServiceConfig is one service's applied configuration.
type ServiceConfig struct {
	ServiceID string
	Rev       string
	Env       map[string]string
	UpdatedAt time.Time
}

// GetServiceConfig reads the applied config; missing rows return Rev "".
func (s *Store) GetServiceConfig(serviceID string) (ServiceConfig, error) {
	var out ServiceConfig
	var envRaw string
	err := s.pool.QueryRow(context.Background(),
		`SELECT service_id, rev, env::text, updated_at FROM service_config WHERE service_id = $1`,
		serviceID).Scan(&out.ServiceID, &out.Rev, &envRaw, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceConfig{ServiceID: serviceID, Env: map[string]string{}}, nil
	}
	if err != nil {
		return ServiceConfig{}, err
	}
	out.Env = map[string]string{}
	_ = json.Unmarshal([]byte(envRaw), &out.Env)
	return out, nil
}

// ApplyServiceConfig atomically swaps env when the stored rev matches
// baseRev ("" matches a missing row). Returns the new rev, or an error
// carrying the current rev on conflict.
func (s *Store) ApplyServiceConfig(serviceID, baseRev string, env map[string]string) (string, error) {
	raw, _ := json.Marshal(env)
	newRev := strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx := context.Background()
	var got string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO service_config (service_id, rev, env, updated_at)
		 VALUES ($1, $2, $3::jsonb, now())
		 ON CONFLICT (service_id) DO UPDATE
		   SET rev = EXCLUDED.rev, env = EXCLUDED.env, updated_at = now()
		 WHERE service_config.rev = $4
		 RETURNING rev`,
		serviceID, newRev, string(raw), baseRev).Scan(&got)
	if err != nil {
		// Either a rev conflict (no row returned) or a real failure.
		// Re-read to tell them apart; a read failure surfaces the error.
		cur, rerr := s.GetServiceConfig(serviceID)
		if rerr != nil {
			return "", err
		}
		return cur.Rev, &RevConflictError{Current: cur.Rev}
	}
	return got, nil
}

// RevConflictError reports a stale base revision.
type RevConflictError struct {
	Current string
}

func (e *RevConflictError) Error() string {
	return "config-push: stale base rev (current " + e.Current + "): re-read and retry"
}
