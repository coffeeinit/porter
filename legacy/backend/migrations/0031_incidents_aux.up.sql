-- 0031_incidents_aux: incident records (alerts escalate here with severity,
-- timeline, and postmortem) and auxiliary VM pairings (builder/browser/
-- bastion companions bound to one workload). Additive only.
CREATE TABLE IF NOT EXISTS incidents (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  severity    TEXT NOT NULL DEFAULT 'info',
  state       TEXT NOT NULL DEFAULT 'open',
  summary     TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_incidents_project ON incidents(project_id, state);

CREATE TABLE IF NOT EXISTS aux_vms (
  id          TEXT PRIMARY KEY,
  workload_id TEXT NOT NULL,
  kind        TEXT NOT NULL,
  node_id     TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_aux_vms_workload ON aux_vms(workload_id);
