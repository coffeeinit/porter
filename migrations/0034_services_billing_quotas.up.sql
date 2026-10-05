-- 0034_services_billing_quotas: operator-managed service templates (custom
-- one-click DBs/apps; built-ins in code are fallback only), scheduled
-- backups (cron + retention, executed by the task runner), and plan limits
-- backing quota enforcement (max_projects/max_vms/max_mem_mib per plan).
CREATE TABLE IF NOT EXISTS service_templates (
  name        TEXT PRIMARY KEY,
  image       TEXT NOT NULL,
  version     TEXT NOT NULL DEFAULT 'latest',
  ports       INT[] NOT NULL DEFAULT '{}',
  volume_mib  INT NOT NULL DEFAULT 1024,
  env         JSONB NOT NULL DEFAULT '{}',
  secrets     TEXT[] NOT NULL DEFAULT '{}',
  description TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS backup_schedules (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id TEXT NOT NULL,
  workload   TEXT NOT NULL DEFAULT '',
  cron       TEXT NOT NULL,
  retention  INT NOT NULL DEFAULT 3,
  enabled    BOOLEAN NOT NULL DEFAULT TRUE,
  last_run   TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_backup_sched_project ON backup_schedules(project_id, enabled);
CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_sched_unique ON backup_schedules(project_id, workload);
CREATE TABLE IF NOT EXISTS plan_limits (
  plan_id TEXT NOT NULL,
  key     TEXT NOT NULL,
  value   BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (plan_id, key)
);
