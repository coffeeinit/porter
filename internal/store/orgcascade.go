// This file implements the destructive side of org deletion (SRS §55):
// one transaction, a dependency check on running VMs, explicit per-table
// counts, and a hard refusal for default orgs. Destructive operations here
// provide RBAC (route perm), audit (audit middleware), and dependency checks
// before any row is removed.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrOrgHasRunningVMs is returned when the org still runs VMs and force
	// was not requested. The API surfaces it as 409.
	ErrOrgHasRunningVMs = errors.New("organization has running VMs")

	// ErrOrgIsDefault guards the auto-created default org (never deletable).
	ErrOrgIsDefault = errors.New("cannot delete the default organization")
)

// DeleteOrgCascade deletes orgID and every row scoped to it in ONE
// transaction, returning per-table deleted-row counts.
//
// Refusals (nothing deleted):
//   - default orgs (ErrOrgIsDefault);
//   - running VMs across the org's projects while force is false
//     (ErrOrgHasRunningVMs).
//
// Verified real schema (0001/0003/0016/0017): orgs, org_members, groups,
// projects(org_id), environments, deployments, dns_records, volumes,
// networks, secrets (all project-scoped, ON DELETE CASCADE), replicas
// (project_id + legacy service_id), micro_vms(replica_id TEXT), plus
// TEXT-keyed aux tables without FKs (usage_events, billing_subscriptions,
// backup_schedules) that would otherwise orphan.
func (s *Store) DeleteOrgCascade(orgID string, force bool) (map[string]int64, error) {
	deleted := map[string]int64{}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("delete org %s: begin tx: %w", orgID, err)
	}
	defer tx.Rollback(ctx)

	var name string
	var isDefault bool
	err = tx.QueryRow(ctx,
		`SELECT name, is_default FROM orgs WHERE id::text = $1`, orgID).
		Scan(&name, &isDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("delete org %s: not found", orgID)
	}
	if err != nil {
		return nil, fmt.Errorf("delete org %s: load: %w", orgID, err)
	}
	if isDefault {
		return nil, ErrOrgIsDefault
	}

	// Dependency check: any running replica directly linked to an org project
	// or legacy-linked through its service blocks deletion unless forced.
	const runningSQL = `SELECT count(*) FROM replicas WHERE state = 'running' AND (
		project_id IN (SELECT id FROM projects WHERE org_id::text = $1)
		OR service_id IN (SELECT id FROM services WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)))`
	var running int64
	if err := tx.QueryRow(ctx, runningSQL, orgID).Scan(&running); err != nil {
		return nil, fmt.Errorf("delete org %s: count running VMs: %w", orgID, err)
	}
	if running > 0 && !force {
		return nil, ErrOrgHasRunningVMs
	}

	// del runs one scoped DELETE and records its row count under label.
	del := func(label, sql string) error {
		tag, err := tx.Exec(ctx, sql, orgID)
		if err != nil {
			return fmt.Errorf("delete org %s: %s: %w", orgID, label, err)
		}
		deleted[label] = tag.RowsAffected()
		return nil
	}

	// micro_vms first: the join needs the replica rows still present.
	if err := del("micro_vms", `DELETE FROM micro_vms WHERE replica_id IN (
		SELECT id::text FROM replicas WHERE
			project_id IN (SELECT id FROM projects WHERE org_id::text = $1)
			OR service_id IN (SELECT id FROM services WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)))`); err != nil {
		return nil, err
	}
	// Legacy service-linked replicas would cascade with services; deleting
	// them explicitly keeps the count honest.
	if err := del("replicas", `DELETE FROM replicas WHERE
		project_id IN (SELECT id FROM projects WHERE org_id::text = $1)
		OR service_id IN (SELECT id FROM services WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1))`); err != nil {
		return nil, err
	}
	for _, d := range []struct{ label, sql string }{
		{"domains", `DELETE FROM domains WHERE
			project_id IN (SELECT id FROM projects WHERE org_id::text = $1)
			OR service_id IN (SELECT id FROM services WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1))`},
		{"volumes", `DELETE FROM volumes WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"dns_records", `DELETE FROM dns_records WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"environments", `DELETE FROM environments WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"deployments", `DELETE FROM deployments WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"secrets", `DELETE FROM secrets WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"networks", `DELETE FROM networks WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		{"services", `DELETE FROM services WHERE project_id IN (SELECT id FROM projects WHERE org_id::text = $1)`},
		// TEXT-keyed aux tables have no FK to projects; clean them so the
		// org leaves no orphaned rows behind.
		{"usage_events", `DELETE FROM usage_events WHERE project_id IN (SELECT id::text FROM projects WHERE org_id::text = $1)`},
		{"billing_subscriptions", `DELETE FROM billing_subscriptions WHERE project_id IN (SELECT id::text FROM projects WHERE org_id::text = $1)`},
		{"backup_schedules", `DELETE FROM backup_schedules WHERE project_id IN (SELECT id::text FROM projects WHERE org_id::text = $1)`},
		{"projects", `DELETE FROM projects WHERE org_id::text = $1`},
		{"org_members", `DELETE FROM org_members WHERE org_id::text = $1`},
		{"groups", `DELETE FROM groups WHERE org_id::text = $1`},
		{"orgs", `DELETE FROM orgs WHERE id::text = $1`},
	} {
		if err := del(d.label, d.sql); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("delete org %s: commit: %w", orgID, err)
	}
	return deleted, nil
}
