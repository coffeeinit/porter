package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// WithTenant runs fn inside a transaction with app.tenant_id set (SET LOCAL),
// so the 0019 FORCE RLS policies actually filter (T4b enforcement). Outside a
// tenant (""), behavior is unchanged (permissive-when-unset branch).
// Secrets MUST go through this path; other tenancy tables follow as threaded.
func (s *Store) WithTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	if s.pool == nil {
		return pgx.ErrNoRows
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListVolumesTx lists volumes for a project with the RLS tenant enforced
// (0021 tightens the 0019 permissive policy now that project_id exists).
func (s *Store) ListVolumesTx(ctx context.Context, tenantID, projectID string) ([]map[string]string, error) {
	out := []map[string]string{}
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, name FROM volumes WHERE project_id = $1 ORDER BY name`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				continue
			}
			out = append(out, map[string]string{"id": id, "name": name})
		}
		return rows.Err()
	})
	return out, err
}

// ListSecretsTx lists secrets for a project with the RLS tenant enforced at
// the query layer (defense in depth over the API middleware check).
func (s *Store) ListSecretsTx(ctx context.Context, tenantID, projectID string) ([]map[string]string, error) {
	out := []map[string]string{}
	err := s.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, name FROM secrets WHERE project_id = $1 ORDER BY name`, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				continue
			}
			out = append(out, map[string]string{"id": id, "name": name})
		}
		return rows.Err()
	})
	return out, err
}
