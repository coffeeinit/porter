-- Porter consolidated migration, step 1: base schema (formerly 0001..0008)
-- 0001_init: Porter v1 PostgreSQL schema (authoritative — Section 4.2).
-- Projects (compose or single_image), services, microVMs, domains, volumes,
-- ssh_keys, and the Phase-8 servers scaffold.

CREATE EXTENSION IF NOT EXISTS pgcrypto; -- for gen_random_uuid()

CREATE TABLE IF NOT EXISTS projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('compose', 'single_image')),
    compose_yaml    TEXT,                      -- null for single_image
    bridge_subnet   CIDR NOT NULL,              -- e.g. 10.42.3.0/24, allocated at create time
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','deploying','running','degraded','stopped','failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS services (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    image           TEXT NOT NULL,
    vcpus           INT NOT NULL DEFAULT 1,
    mem_mib         INT NOT NULL DEFAULT 256,
    replicas_desired INT NOT NULL DEFAULT 1 CHECK (replicas_desired >= 1),
    restart_policy  TEXT NOT NULL DEFAULT 'on-failure',
    healthcheck     JSONB,                      -- {type: http|tcp, path, port, interval_s}
    env             JSONB NOT NULL DEFAULT '{}',
    depends_on      TEXT[] NOT NULL DEFAULT '{}',
    UNIQUE (project_id, name)
);

CREATE TABLE IF NOT EXISTS vms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id      UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    replica_index   INT NOT NULL DEFAULT 0,
    container_id    TEXT,                       -- legacy runtime ID, empty for direct Firecracker
    task_id         TEXT,                       -- legacy task ID, empty for direct Firecracker
    state           TEXT NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending','booting','running','stopping','stopped','failed')),
    health_status   TEXT NOT NULL DEFAULT 'checking'
                    CHECK (health_status IN ('healthy','unhealthy','checking')),
    ip_address      INET,
    mac_address     MACADDR,
    ports           JSONB NOT NULL DEFAULT '[]', -- [{container_port, host_port, protocol}]
    started_at      TIMESTAMPTZ,
    crashed         BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, replica_index)
);

CREATE TABLE IF NOT EXISTS domains (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id      UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    domain          TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('stable','preview','custom')),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS volumes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    size_mib        INT NOT NULL,
    attached_vm_id  UUID REFERENCES vms(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ssh_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    label           TEXT NOT NULL,
    public_key      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS servers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        TEXT NOT NULL UNIQUE,
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_vms_service_id    ON vms(service_id);
CREATE INDEX IF NOT EXISTS idx_vms_state         ON vms(state);
CREATE INDEX IF NOT EXISTS idx_services_project  ON services(project_id);
CREATE INDEX IF NOT EXISTS idx_domains_service   ON domains(service_id);

-- 0002_store_columns: adds a JSONB `data` blob to every core table so the Go
-- store can round-trip domain structs faithfully while the typed columns from
-- 0001 remain populated for SQL queries. Also adds the `users` and `services`
-- tables the store and API need (0001 covered ssh_keys/servers only).

ALTER TABLE projects ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';
ALTER TABLE vms      ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';
ALTER TABLE domains  ADD COLUMN IF NOT EXISTS vm_id UUID;
ALTER TABLE domains  ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';
ALTER TABLE volumes  ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';
ALTER TABLE servers  ADD COLUMN IF NOT EXISTS data JSONB NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL UNIQUE,
    role          TEXT NOT NULL DEFAULT 'admin',
    password_hash TEXT NOT NULL,
    salt          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    data          JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS services (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    data       JSONB NOT NULL DEFAULT '{}',
    UNIQUE (project_id, name)
);

CREATE INDEX IF NOT EXISTS idx_vms_data_state      ON vms(state);
CREATE INDEX IF NOT EXISTS idx_domains_vm_id       ON domains(vm_id);
CREATE INDEX IF NOT EXISTS idx_services_project_id ON services(project_id);

-- 0003_secrets: per-project secrets for v0.2 (env/secrets management).
-- value is stored as an opaque blob; the application encrypts before write
-- (e.g. with a key derived from the porter.toml api token).
CREATE TABLE IF NOT EXISTS secrets (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    value_encrypted  BYTEA NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_secrets_project ON secrets(project_id);

-- 0004_deployments: version history / rollback for v0.3 Application Platform.
CREATE TABLE IF NOT EXISTS deployments (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    revision      BIGSERIAL,
    git_url       TEXT,
    git_commit    TEXT,
    build_status  TEXT NOT NULL DEFAULT 'pending'
                  CHECK (build_status IN ('pending','building','ready','failed')),
    image_digest  TEXT,
    manifest      JSONB NOT NULL DEFAULT '{}',
    rollback_to   UUID REFERENCES deployments(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_deployments_project ON deployments(project_id);

-- 0005_networks: per-project overlay/bridge networks for v0.6 Networking.
CREATE TABLE IF NOT EXISTS networks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    cidr       CIDR,
    plugin     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_networks_project ON networks(project_id);

-- 0006_golden_images: reusable VM templates / direct image library for v0.4.
-- Deployable images resolve to a host rootfs/kernel manifest in the JSON data.
CREATE TABLE IF NOT EXISTS golden_images (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    image       TEXT NOT NULL,          -- stable catalog ref, e.g. custom://redis
    description TEXT NOT NULL DEFAULT '',
    vcpus       INT NOT NULL DEFAULT 1,
    mem_mib     INT NOT NULL DEFAULT 256,
    ports       JSONB NOT NULL DEFAULT '[]',
    env         JSONB NOT NULL DEFAULT '{}',
    tags        TEXT[] NOT NULL DEFAULT '{}',
    logo        TEXT NOT NULL DEFAULT '', -- image URL for the dashboard tile
    version     TEXT NOT NULL DEFAULT 'latest',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_golden_images_name ON golden_images(name);

-- 0007_metrics: time-series samples for v0.9 Observability.
CREATE TABLE IF NOT EXISTS metrics_samples (
    id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id    UUID REFERENCES vms(id) ON DELETE CASCADE,
    metric   TEXT NOT NULL,
    value    DOUBLE PRECISION NOT NULL,
    ts       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_metrics_vm_ts ON metrics_samples(vm_id, metric, ts);

-- 0008_health_events: health history feed for the dashboard.
CREATE TABLE IF NOT EXISTS health_events (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id      UUID REFERENCES vms(id) ON DELETE CASCADE,
    service_id UUID REFERENCES services(id) ON DELETE CASCADE,
    status     TEXT NOT NULL,          -- healthy | unhealthy | checking
    detail     TEXT NOT NULL DEFAULT '',
    ts         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_health_events_vm_ts ON health_events(vm_id, ts);
