-- Phase 2: v3 system (formerly 0009..0013)
-- 0009_orgs_dns_buildlogs: project=VM model support.
-- Projects can be grouped into orgs and tagged; each project carries DNS
-- records and a rolling build log; volumes are /mnt mount points.

CREATE TABLE IF NOT EXISTS orgs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES orgs(id) ON DELETE SET NULL;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS dns_records (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,          -- e.g. web.myapp.porter.test
    type       TEXT NOT NULL DEFAULT 'A',
    value      TEXT NOT NULL,          -- IP or CNAME target
    ttl        INT NOT NULL DEFAULT 300,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE TABLE IF NOT EXISTS build_logs (
    id         BIGSERIAL PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    line       TEXT NOT NULL,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_build_logs_project ON build_logs(project_id, id);
CREATE INDEX IF NOT EXISTS idx_dns_records_project ON dns_records(project_id);

-- 0010_v3_full_schema: the complete v3 model (API Endpoint Catalog v3).
-- Project = microVM app. Replicas live inside their project. Every user has
-- an auto-created default org; groups are folders inside an org. No managed
-- volumes (persistence is a host_mount_path on the project); no image catalog
-- (image is a plain OCI ref).
--
-- This file is idempotent over the earlier migrations (0001-0009): it creates
-- every table the v3 API needs and adds v3 columns to any table the old schema
-- already created.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- --- orgs: every user auto-gets one (is_default=true); extras are opt-in ---
CREATE TABLE IF NOT EXISTS orgs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    owner_id    UUID NOT NULL,                          -- the user the default org was created for
    is_default  BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, is_default)                        -- at most one default org per owner
);

CREATE TABLE IF NOT EXISTS org_members (
    org_id  UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    role    TEXT NOT NULL DEFAULT 'member',
    PRIMARY KEY (org_id, user_id)
);

-- --- groups: lightweight folders for related projects, scoped to an org ---
CREATE TABLE IF NOT EXISTS groups (
    id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id  UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS group_projects (
    group_id   UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, project_id)
);

-- --- projects = the microVM app (services/volumes tables from 0001 are obsolete) ---
ALTER TABLE projects ADD COLUMN IF NOT EXISTS org_id UUID REFERENCES orgs(id) ON DELETE CASCADE;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS image TEXT;               -- set for single_image
ALTER TABLE projects ADD COLUMN IF NOT EXISTS host_mount_path TEXT;     -- optional bind mount (no managed volumes)
ALTER TABLE projects ADD COLUMN IF NOT EXISTS replicas_desired INT NOT NULL DEFAULT 1 CHECK (replicas_desired >= 1);
ALTER TABLE projects ADD COLUMN IF NOT EXISTS restart_policy TEXT NOT NULL DEFAULT 'on-failure';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS healthcheck JSONB;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS env JSONB NOT NULL DEFAULT '{}';

-- --- replicas: one row per Firecracker microVM (renamed from 0001's vms) ---
DO $$
BEGIN
  -- Idempotent vms -> replicas rename: only run when the old table exists and
  -- the new one does not (re-running against a partially-migrated or
  -- already-consolidated DB must not fail).
  IF to_regclass('public.vms') IS NOT NULL AND to_regclass('public.replicas') IS NULL THEN
    ALTER TABLE vms RENAME TO replicas;
  END IF;
END
$$;
ALTER TABLE replicas ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX IF NOT EXISTS uq_replicas_project_index ON replicas(project_id, replica_index);

-- --- domains: attached to a project (routes to its replica pool) ---
ALTER TABLE domains ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE CASCADE;

-- --- users (bootstrap admin + additional accounts) ---
CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL UNIQUE,
    role          TEXT NOT NULL DEFAULT 'admin',
    password_hash TEXT NOT NULL,
    salt          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- deployments: version history / rollback ---
CREATE TABLE IF NOT EXISTS deployments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    revision      BIGSERIAL,
    build_status  TEXT NOT NULL DEFAULT 'pending' CHECK (build_status IN ('pending','building','ready','failed')),
    image_digest  TEXT,
    rollback_to   UUID REFERENCES deployments(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- secrets: per-project, stored encrypted (app-side) ---
CREATE TABLE IF NOT EXISTS secrets (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    value_encrypted  BYTEA NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- networks: per-project bridge/overlay networks ---
CREATE TABLE IF NOT EXISTS networks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    cidr       CIDR,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- dns_records: per project (project = VM) ---
CREATE TABLE IF NOT EXISTS dns_records (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL DEFAULT 'A',
    value      TEXT NOT NULL,
    ttl        INT NOT NULL DEFAULT 300,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- build_logs: rolling per-project build log ---
CREATE TABLE IF NOT EXISTS build_logs (
    id         BIGSERIAL PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    line       TEXT NOT NULL,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- golden_images: image library entries (image is an OCI ref/URL) ---
CREATE TABLE IF NOT EXISTS golden_images (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    image       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    vcpus       INT NOT NULL DEFAULT 1,
    mem_mib     INT NOT NULL DEFAULT 256,
    ports       JSONB NOT NULL DEFAULT '[]',
    env         JSONB NOT NULL DEFAULT '{}',
    tags        TEXT[] NOT NULL DEFAULT '{}',
    logo        TEXT NOT NULL DEFAULT '',
    version     TEXT NOT NULL DEFAULT 'latest',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- metrics + health_events: v0.9 observability ---
CREATE TABLE IF NOT EXISTS metrics_samples (
    id     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id  UUID REFERENCES replicas(id) ON DELETE CASCADE,
    metric TEXT NOT NULL,
    value  DOUBLE PRECISION NOT NULL,
    ts     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS health_events (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id      UUID REFERENCES replicas(id) ON DELETE CASCADE,
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    status     TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- servers: Phase-8 multi-host scaffold ---
CREATE TABLE IF NOT EXISTS servers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname      TEXT NOT NULL UNIQUE,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- index support ---
CREATE INDEX IF NOT EXISTS idx_replicas_project   ON replicas(project_id);
CREATE INDEX IF NOT EXISTS idx_replicas_state     ON replicas(state);
CREATE INDEX IF NOT EXISTS idx_domains_project    ON domains(project_id);
CREATE INDEX IF NOT EXISTS idx_domains_domain     ON domains(domain);
CREATE INDEX IF NOT EXISTS idx_dns_project        ON dns_records(project_id);
CREATE INDEX IF NOT EXISTS idx_build_logs_project ON build_logs(project_id, id);
CREATE INDEX IF NOT EXISTS idx_deployments_project ON deployments(project_id);
CREATE INDEX IF NOT EXISTS idx_metrics_vm_ts      ON metrics_samples(vm_id, metric, ts);
CREATE INDEX IF NOT EXISTS idx_groups_org         ON groups(org_id);
CREATE INDEX IF NOT EXISTS idx_gp_project         ON group_projects(project_id);

-- 0011_full_paas_schema: complete Vercel/Fly-style PaaS surface (microVM engine).
--
-- Design decisions:
--  * Docker Compose = a "stack". Each compose SERVICE becomes its OWN project
--    (its own microVM pool), grouped under the parent stack via stacks + the
--    projects.stack_id column. Services are never nested "inside" a project —
--    a project is always one app = one microVM pool.
--  * Every handler in the 256-endpoint api.go route catalog has a table:
--    orgs/teams/groups/memberships, projects, replicas, deployments, domains/DNS,
--    env/secrets, compose stacks, settings, environments, hooks, crons, drains,
--    alerts, redirects, analytics, firewall, cache, volumes, images, api_keys.
--  * Idempotent over 0001-0010 (CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS).

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Legacy tables from 0001/0005/0010 used a different (v1/v2) shape and can't be
-- reconciled with the full PaaS schema via ADD COLUMN:
--   * 0001 `volumes`  has attached_vm_id, no project_id / mount_path / status
--   * 0010 `networks` has no `driver` column
-- This migration owns their final schema, so drop the legacy versions first.
DROP TABLE IF EXISTS volumes CASCADE;
DROP TABLE IF EXISTS networks CASCADE;

-- --- stacks: a docker-compose project is a named stack of service-projects ---
CREATE TABLE IF NOT EXISTS stacks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    org_id      UUID REFERENCES orgs(id) ON DELETE CASCADE,
    source      TEXT NOT NULL DEFAULT 'compose',        -- compose | git | image | ml
    compose_yaml TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS stack_id UUID REFERENCES stacks(id) ON DELETE SET NULL;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'image'; -- image|compose|git|ml
ALTER TABLE projects ADD COLUMN IF NOT EXISTS model TEXT;                           -- ML model ref (gpu/batch serving)
ALTER TABLE projects ADD COLUMN IF NOT EXISTS gpu TEXT;                             -- e.g. "nvidia-t4" or ""
ALTER TABLE projects ADD COLUMN IF NOT EXISTS compose_service TEXT;                 -- this project's service name inside its stack
ALTER TABLE projects ADD COLUMN IF NOT EXISTS networks JSONB NOT NULL DEFAULT '[]';  -- per-project bridge networks

-- --- replicas: one row per Firecracker microVM (extends 0010's replicas) ---
ALTER TABLE replicas ADD COLUMN IF NOT EXISTS console_port INT;
ALTER TABLE replicas ADD COLUMN IF NOT EXISTS ssh_enabled BOOLEAN NOT NULL DEFAULT false;

-- --- api_keys: per-user long-lived tokens ---
CREATE TABLE IF NOT EXISTS api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL,           -- sha256 hex of the raw key
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

-- --- volumes: managed persistent storage mounted into a microVM ---
CREATE TABLE IF NOT EXISTS volumes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    size_mib    INT NOT NULL DEFAULT 1024,
    mount_path  TEXT NOT NULL DEFAULT '/data',
    status      TEXT NOT NULL DEFAULT 'provisioning', -- provisioning|mounted|detached|failed
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- alerts: threshold alerts on VM/metrics ---
CREATE TABLE IF NOT EXISTS alerts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    metric     TEXT NOT NULL,             -- cpu | mem | health | traffic
    threshold  DOUBLE PRECISION NOT NULL,
    op         TEXT NOT NULL DEFAULT '>',
    cooldown_s INT NOT NULL DEFAULT 300,
    silenced   BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- hooks: outbound webhooks on lifecycle events ---
CREATE TABLE IF NOT EXISTS hooks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    url        TEXT NOT NULL,
    events     TEXT[] NOT NULL DEFAULT '{}',  -- e.g. {deploy,state,health}
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- crons: scheduled jobs (run an image as a short-lived microVM) ---
CREATE TABLE IF NOT EXISTS crons (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,             -- cron expression
    job_image   TEXT NOT NULL,             -- OCI image to boot for the job
    active      BOOLEAN NOT NULL DEFAULT true,
    last_run_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- drains: log/event shipping endpoints ---
CREATE TABLE IF NOT EXISTS drains (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    endpoint   TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'http',  -- http | s3 | syslog
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- redirects: domain redirect rules ---
CREATE TABLE IF NOT EXISTS redirects (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source     TEXT NOT NULL,
    target     TEXT NOT NULL,
    permanent  BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- firewall_rules: per-project ingress/egress rules ---
CREATE TABLE IF NOT EXISTS firewall_rules (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    direction  TEXT NOT NULL DEFAULT 'ingress',
    action     TEXT NOT NULL DEFAULT 'allow',     -- allow | deny
    proto      TEXT NOT NULL DEFAULT 'tcp',
    ports      TEXT NOT NULL DEFAULT '',          -- "80,443" or "8000-9000"
    source     TEXT NOT NULL DEFAULT '0.0.0.0/0',
    priority   INT NOT NULL DEFAULT 100,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- environments: deploy environments (prod/staging/preview, Vercel-style) ---
CREATE TABLE IF NOT EXISTS environments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT 'main',
    url        TEXT NOT NULL DEFAULT '',
    env_domain TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- project_settings: per-section JSON settings for every /settings/* route ---
CREATE TABLE IF NOT EXISTS project_settings (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    section    TEXT NOT NULL,               -- general|build|checks|rollout|framework|security|...
    data       JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, section)
);

-- --- project_members: per-project team membership/roles (Vercel team parity) ---
CREATE TABLE IF NOT EXISTS project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL,
    role       TEXT NOT NULL DEFAULT 'member',  -- owner|admin|developer|member
    invited    BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

-- --- build records: git-based builds → OCI image → microVM ---
CREATE TABLE IF NOT EXISTS builds (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    git_url     TEXT NOT NULL DEFAULT '',
    branch      TEXT NOT NULL DEFAULT 'main',
    build_status TEXT NOT NULL DEFAULT 'queued', -- queued|building|ready|failed
    image       TEXT NOT NULL DEFAULT '',
    log         TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- networks: per-project bridge networks (docker-ecosystem parity) ---
CREATE TABLE IF NOT EXISTS networks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    cidr       TEXT NOT NULL DEFAULT '10.42.0.0/24',
    driver     TEXT NOT NULL DEFAULT 'bridge',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- --- indices ---
CREATE INDEX IF NOT EXISTS idx_volumes_project    ON volumes(project_id);
CREATE INDEX IF NOT EXISTS idx_alerts_project     ON alerts(project_id);
CREATE INDEX IF NOT EXISTS idx_hooks_project      ON hooks(project_id);
CREATE INDEX IF NOT EXISTS idx_crons_project      ON crons(project_id);
CREATE INDEX IF NOT EXISTS idx_drains_project     ON drains(project_id);
CREATE INDEX IF NOT EXISTS idx_redirects_project  ON redirects(project_id);
CREATE INDEX IF NOT EXISTS idx_firewall_project   ON firewall_rules(project_id);
CREATE INDEX IF NOT EXISTS idx_environments_proj  ON environments(project_id);
CREATE INDEX IF NOT EXISTS idx_builds_project     ON builds(project_id);
CREATE INDEX IF NOT EXISTS idx_projects_stack     ON projects(stack_id);

-- 0012: columns the store INSERTs that earlier migrations (0009/0010) omitted.
--   * orgs          -> owner_id (TEXT: the owning username — the auth model keys
--                       users by username, e.g. the bootstrap "admin"; a UUID FK
--                       to users.id breaks that), is_default
--   * health_events -> project_id (AddHealthEvent / ListHealthEvents)
-- Idempotent and safe on existing data.

ALTER TABLE orgs DROP COLUMN IF EXISTS owner_id;
ALTER TABLE orgs ADD COLUMN IF NOT EXISTS owner_id TEXT;
ALTER TABLE orgs ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE health_events ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_orgs_owner         ON orgs(owner_id);
CREATE INDEX IF NOT EXISTS idx_health_events_proj ON health_events(project_id);

-- 0013: legacy NOT NULL columns (from 0001/0010) that the v3 store never
-- populates — its inserts omit them (state lives in the row's `data` JSON
-- blob). Drop the NOT NULL so inserts succeed:
--   * projects.bridge_subnet   (subnet is allocated by the handler, stored in data)
--   * replicas.service_id      (legacy v1 services FK; store uses project_id)
--   * domains.service_id       (legacy v1 services FK; store uses project_id)
-- Idempotent and safe on existing data.

ALTER TABLE projects ALTER COLUMN bridge_subnet DROP NOT NULL;
ALTER TABLE replicas ALTER COLUMN service_id DROP NOT NULL;
ALTER TABLE domains ALTER COLUMN service_id DROP NOT NULL;

