-- Phase 3: full PaaS (formerly 0014..0020)
-- 0014: the store's AddDomain maps types.Domain.Type -> `kind` and
-- types.Domain.Status -> `status`, which the legacy CHECK constraints reject
-- for empty/unknown values. The row's `data` JSON is authoritative, so drop
-- the constraints (the app manages kinds/statuses).
ALTER TABLE domains DROP CONSTRAINT IF EXISTS domains_kind_check;
ALTER TABLE domains DROP CONSTRAINT IF EXISTS domains_status_check;

-- 0015: ListGoldenImages reads the row's `data` JSON blob (the store's
-- convention), but the golden_images table never got a data column.
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS data JSONB;

-- 0016: seed the golden-image library (redis / postgresql / mysql) so the
-- dashboard's image picker has examples on a fresh install. These rows are
-- metadata placeholders until a matching direct rootfs/kernel artifact is
-- registered. Idempotent: ON CONFLICT (name).
INSERT INTO golden_images (id, name, image, description, vcpus, mem_mib, ports, env, tags, logo, version, data, created_at)
VALUES
	  (gen_random_uuid(), 'redis',     'custom://redis',    'Redis 7 direct image metadata; register rootfs.ext4 + vmlinux before deploy',    1, 256, '[{"container_port":6379}]'::jsonb, '{}'::jsonb, ARRAY['cache','redis'],     '', 'v1', '{"name":"redis","image":"custom://redis","vcpus":1,"mem_mib":256,"ports":[{"container_port":6379}]}'::jsonb, now()),
	  (gen_random_uuid(), 'postgresql','custom://postgresql','PostgreSQL direct image metadata; register rootfs.ext4 + vmlinux before deploy',1, 512, '[{"container_port":5432}]'::jsonb, '{}'::jsonb, ARRAY['db','postgres'],    '', 'v1', '{"name":"postgresql","image":"custom://postgresql","vcpus":1,"mem_mib":512,"ports":[{"container_port":5432}]}'::jsonb, now()),
	  (gen_random_uuid(), 'mysql',     'custom://mysql',   'MySQL direct image metadata; register rootfs.ext4 + vmlinux before deploy',     1, 512, '[{"container_port":3306}]'::jsonb, '{}'::jsonb, ARRAY['db','mysql'],       '', 'v1', '{"name":"mysql","image":"custom://mysql","vcpus":1,"mem_mib":512,"ports":[{"container_port":3306}]}'::jsonb, now())
ON CONFLICT (name) DO NOTHING;

-- 0017: durable daemon/audit log. The in-memory ring stays the fast path for
-- the dashboard; this table is the audit trail (retention is an ops choice).
CREATE TABLE IF NOT EXISTS daemon_logs (
    id   BIGSERIAL PRIMARY KEY,
    ts   TIMESTAMPTZ NOT NULL DEFAULT now(),
    line TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_daemon_logs_ts ON daemon_logs(ts);

-- 0018_deploy_status_open.up.sql
-- Release the deployment build_status CHECK so the real preview/live lifecycle
-- can persist, and allow projects to scale to 0 (paused) replicas.

ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_build_status_check;
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_replicas_desired_check;

ALTER TABLE deployments ADD CONSTRAINT deployments_build_status_check
    CHECK (build_status IN ('pending','building','queued','ready','failed','preview','live'));

ALTER TABLE projects ADD CONSTRAINT projects_replicas_desired_check
    CHECK (replicas_desired >= 0);

-- 0019_observability_tables.up.sql
-- Persist observability data that previously lived only in the in-memory rings
-- (lost on restart): per-replica traffic, per-replica logs, and pre-aggregated
-- daily analytics. The rings stay as the dashboard's hot path; these tables are
-- the durable store the analytics/audit endpoints read from.

-- traffic_log: one row per proxied request (written by the gateway).
CREATE TABLE IF NOT EXISTS traffic_logs (
    id          BIGSERIAL PRIMARY KEY,
    vm_id       UUID,
    project_id  UUID,
    method      TEXT NOT NULL DEFAULT 'GET',
    host        TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',
    status      INT NOT NULL DEFAULT 200,
    duration_ms INT NOT NULL DEFAULT 0,
    remote_ip   TEXT NOT NULL DEFAULT '',
    ts          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_traffic_logs_vm  ON traffic_logs(vm_id);
CREATE INDEX IF NOT EXISTS idx_traffic_logs_proj ON traffic_logs(project_id, ts);

-- vm_logs: durable per-VM log tail (mirrors the in-memory ring).
CREATE TABLE IF NOT EXISTS vm_logs (
    id         BIGSERIAL PRIMARY KEY,
    vm_id      UUID,
    project_id UUID,
    line       TEXT NOT NULL DEFAULT '',
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_vm_logs_vm ON vm_logs(vm_id, ts DESC);

-- analytics_daily: pre-aggregated per-project request/bandwidth counters per day
-- (what the analytics endpoints read for historical usage).
CREATE TABLE IF NOT EXISTS analytics_daily (
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    day          DATE NOT NULL,
    requests     BIGINT NOT NULL DEFAULT 0,
    bandwidth    BIGINT NOT NULL DEFAULT 0,
    invocations  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, day)
);

-- daemon_logs_history: durable daemon log (complements the ring).
CREATE TABLE IF NOT EXISTS daemon_logs_history (
    id   BIGSERIAL PRIMARY KEY,
    line TEXT NOT NULL DEFAULT '',
    ts   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 0020_golden_data.up.sql
-- store.PutGoldenImage persists the full manifest as a JSONB `data` blob; the
-- original golden_images table lacked that column. Add it additively.
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';
