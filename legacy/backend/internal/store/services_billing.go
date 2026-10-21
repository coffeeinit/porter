// Service templates, backup schedules, and plan limits (migration 0034).
// Templates resolve custom-first (operator-managed rows), built-in catalog
// second; schedules drive the task runner's backup checks; limits back
// quota enforcement at admission.
package store

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// ServiceTemplateRow is one operator-managed one-click template.
type ServiceTemplateRow struct {
	Name        string
	Image       string
	Version     string
	Ports       []int
	VolumeMiB   int
	Env         map[string]string
	Secrets     []string
	Description string
}

// PutServiceTemplate upserts a custom template (operator-managed).
func (s *Store) PutServiceTemplate(t ServiceTemplateRow) error {
	env, _ := json.Marshal(t.Env)
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO service_templates (name, image, version, ports, volume_mib, env, secrets, description)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8)
		 ON CONFLICT (name) DO UPDATE SET image = EXCLUDED.image, version = EXCLUDED.version,
		 ports = EXCLUDED.ports, volume_mib = EXCLUDED.volume_mib, env = EXCLUDED.env,
		 secrets = EXCLUDED.secrets, description = EXCLUDED.description`,
		t.Name, t.Image, t.Version, t.Ports, t.VolumeMiB, string(env), t.Secrets, t.Description)
	if err != nil {
		log.Printf("store: put service template: %v", err)
	}
	return err
}

// ListServiceTemplates returns custom templates (built-ins live in code as fallback).
func (s *Store) ListServiceTemplates() []ServiceTemplateRow {
	rows, err := s.pool.Query(context.Background(),
		`SELECT name, image, version, ports, volume_mib, env::text, secrets, description
		 FROM service_templates ORDER BY name`)
	if err != nil {
		log.Printf("store: list service templates: %v", err)
		return nil
	}
	defer rows.Close()
	var out []ServiceTemplateRow
	for rows.Next() {
		var t ServiceTemplateRow
		var envRaw string
		if err := rows.Scan(&t.Name, &t.Image, &t.Version, &t.Ports,
			&t.VolumeMiB, &envRaw, &t.Secrets, &t.Description); err != nil {
			continue
		}
		t.Env = map[string]string{}
		_ = json.Unmarshal([]byte(envRaw), &t.Env)
		out = append(out, t)
	}
	return out
}

// DeleteServiceTemplate removes a custom template (built-ins unaffected).
func (s *Store) DeleteServiceTemplate(name string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM service_templates WHERE name = $1`, name)
	if err != nil {
		log.Printf("store: delete service template: %v", err)
		return false
	}
	return res.RowsAffected() > 0
}

// BackupSchedule is one per-project recurring backup rule.
type BackupSchedule struct {
	ID        string
	ProjectID string
	Workload  string
	Cron      string
	Retention int
	Enabled   bool
	LastRun   *time.Time
}

// PutBackupSchedule creates or replaces a schedule (one per project+workload).
func (s *Store) PutBackupSchedule(projectID, workload, cronExpr string, retention int) (string, error) {
	if retention < 1 {
		retention = 3
	}
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO backup_schedules (project_id, workload, cron, retention, enabled)
		 VALUES ($1,$2,$3,$4,TRUE)
		 ON CONFLICT (project_id, workload) DO UPDATE SET cron = EXCLUDED.cron,
		 retention = EXCLUDED.retention, enabled = TRUE
		 RETURNING id::text`,
		projectID, workload, cronExpr, retention).Scan(&id)
	if err != nil {
		log.Printf("store: put backup schedule: %v", err)
	}
	return id, err
}

// ListBackupSchedules returns enabled schedules due now or all for a project.
func (s *Store) ListBackupSchedules(projectID string, enabledOnly bool) []BackupSchedule {
	q := `SELECT id::text, project_id, workload, cron, retention, enabled, last_run
		 FROM backup_schedules WHERE project_id = $1`
	if enabledOnly {
		q += ` AND enabled = TRUE`
	}
	rows, err := s.pool.Query(context.Background(), q+` ORDER BY created_at`, projectID)
	if err != nil {
		log.Printf("store: list backup schedules: %v", err)
		return nil
	}
	defer rows.Close()
	var out []BackupSchedule
	for rows.Next() {
		var b BackupSchedule
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.Workload, &b.Cron,
			&b.Retention, &b.Enabled, &b.LastRun); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// MarkBackupScheduleRun stamps last_run after a successful scheduled backup.
func (s *Store) MarkBackupScheduleRun(id string, at time.Time) {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE backup_schedules SET last_run = $2 WHERE id::text = $1`, id, at)
	if err != nil {
		log.Printf("store: mark backup schedule: %v", err)
	}
}

// SetPlanLimit upserts one quota limit (key: max_projects, max_vms, max_mem_mib).
func (s *Store) SetPlanLimit(planID, key string, value int64) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO plan_limits (plan_id, key, value) VALUES ($1,$2,$3)
		 ON CONFLICT (plan_id, key) DO UPDATE SET value = EXCLUDED.value`,
		planID, key, value)
	if err != nil {
		log.Printf("store: set plan limit: %v", err)
	}
	return err
}

// PlanLimits returns all limits for a plan (missing keys = unlimited).
func (s *Store) PlanLimits(planID string) map[string]int64 {
	rows, err := s.pool.Query(context.Background(),
		`SELECT key, value FROM plan_limits WHERE plan_id = $1`, planID)
	if err != nil {
		log.Printf("store: plan limits: %v", err)
		return map[string]int64{}
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		out[k] = v
	}
	return out
}
