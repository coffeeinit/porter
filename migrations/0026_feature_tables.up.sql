-- 0026_feature_tables: durable spine for the feature packages added from
-- the FireCrackManager / OpenClawMachines / pve-microvm references.
-- Purely additive: operations (durable tasks + idempotency), placements
-- (reserve/active/released + affinity), capacity_policies (host>pool>
-- provider>global), snapshots/backups (ObjectStore descriptors),
-- workflow_runs/events (durable automation), marketplace_items (catalog),
-- enrollment_tokens (one-time host bootstrap), node_heartbeats (liveness).
-- Postgres is the truth; caches/key state never live here.
--
-- NOTE: durable tasks ride the existing tasks ledger (0018 event spine:
-- UUID ids, lowercase status, lock_key UNIQUE). No second task table.

CREATE TABLE IF NOT EXISTS placements (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workload_id TEXT NOT NULL,
  node_id TEXT NOT NULL DEFAULT '',
  home_node_id TEXT NOT NULL DEFAULT '',
  vm_ip TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'none',
  operation_id TEXT NOT NULL DEFAULT '',
  reserved_at TIMESTAMPTZ,
  activated_at TIMESTAMPTZ,
  released_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_placements_workload ON placements(workload_id, state);
CREATE INDEX IF NOT EXISTS idx_placements_node ON placements(node_id, state);

CREATE TABLE IF NOT EXISTS capacity_policies (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  scope TEXT NOT NULL, -- host | pool | provider | global
  scope_id TEXT NOT NULL DEFAULT '',
  cpu_overcommit DOUBLE PRECISION,
  mem_overcommit DOUBLE PRECISION,
  reserve_vcpu INT,
  reserve_mem_mib INT,
  enabled BOOLEAN,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (scope, scope_id)
);

CREATE TABLE IF NOT EXISTS snapshots (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  vm_id TEXT NOT NULL,
  snap_type TEXT NOT NULL DEFAULT 'Full',
  state_object TEXT NOT NULL DEFAULT '',
  mem_object TEXT NOT NULL DEFAULT '',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  verified BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_snapshots_vm ON snapshots(vm_id, created_at DESC);

CREATE TABLE IF NOT EXISTS backups (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workload_id TEXT NOT NULL,
  object_path TEXT NOT NULL DEFAULT '',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL DEFAULT 'manual',
  verified BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_backups_workload ON backups(workload_id, created_at DESC);

CREATE TABLE IF NOT EXISTS workflow_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  kind TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'QUEUED',
  phase TEXT NOT NULL DEFAULT '',
  input JSONB NOT NULL DEFAULT '{}',
  output JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_kind ON workflow_runs(kind, state);

CREATE TABLE IF NOT EXISTS workflow_events (
  id BIGSERIAL PRIMARY KEY,
  run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_workflow_events_run ON workflow_events(run_id, id);

CREATE TABLE IF NOT EXISTS marketplace_items (
  publisher TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  digest TEXT NOT NULL DEFAULT '',
  manifest_uri TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (publisher, name, version)
);

CREATE TABLE IF NOT EXISTS enrollment_tokens (
  token TEXT PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'customer_owned',
  labels JSONB NOT NULL DEFAULT '{}',
  used_by TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS node_heartbeats (
  node_id TEXT PRIMARY KEY,
  agent_version TEXT NOT NULL DEFAULT '',
  vcpu_free INT NOT NULL DEFAULT 0,
  mem_free_mib INT NOT NULL DEFAULT 0,
  vm_count INT NOT NULL DEFAULT 0,
  capabilities JSONB NOT NULL DEFAULT '{}',
  reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
