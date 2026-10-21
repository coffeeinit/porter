-- =============================================================
-- 0018_event_spine_task_ledger_idempotency: durable operations + audit (G5)
-- plus missing capability seeds (G6) and idempotency keys (T10a).
-- Tables:
--   events         — durable versioned event spine (never deleted by app code)
--   audit_events   — security-sensitive/administrative activity
--   tasks          — durable long-running operations (builds, deploys, ... )
--   idempotency_keys — mutating-write dedupe (key → stored response)
-- Seeds: replica.snapshot/restore, network.*, certificate.* capabilities +
-- role grants (member: snapshot/restore self-serve; admin/owner: full set).
-- =============================================================

CREATE TABLE IF NOT EXISTS events (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,                    -- e.g. vm.created
    version         INT NOT NULL DEFAULT 1,
    payload         JSONB NOT NULL DEFAULT '{}',
    scope_type      TEXT NOT NULL DEFAULT '',
    scope_id        TEXT NOT NULL DEFAULT '',
    actor_type      TEXT NOT NULL DEFAULT '',
    actor_id        TEXT NOT NULL DEFAULT '',
    correlation_id  TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    resource_ref    TEXT NOT NULL DEFAULT '',
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_events_name_time ON events(name, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_scope ON events(scope_type, scope_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_resource ON events(resource_ref, occurred_at DESC);

CREATE TABLE IF NOT EXISTS audit_events (
    id           BIGSERIAL PRIMARY KEY,
    actor_type   TEXT NOT NULL DEFAULT '',
    actor_id     TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL,                        -- e.g. replica.delete
    resource_ref TEXT NOT NULL DEFAULT '',
    request_id   TEXT NOT NULL DEFAULT '',
    ip           INET,
    outcome      TEXT NOT NULL DEFAULT '',            -- allowed | denied | failed
    at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_actor_time ON audit_events(actor_type, actor_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_events(resource_ref, at DESC);

CREATE TABLE IF NOT EXISTS tasks (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         TEXT NOT NULL,                        -- build | deploy | snapshot | ...
    payload      JSONB NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'queued',       -- queued|running|waiting|succeeded|failed|retrying|cancelled|partially_succeeded|needs_attention
    attempts     INT NOT NULL DEFAULT 0,
    lock_key     TEXT UNIQUE,
    progress     JSONB NOT NULL DEFAULT '{}',
    error        TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status, updated_at);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key          TEXT PRIMARY KEY,
    status_code  INT NOT NULL DEFAULT 200,
    response     JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- G6 capability seeds (permissions.id IS the capability string).
INSERT INTO permissions (id, name) VALUES
    ('replica.snapshot',  'Snapshot a replica'),
    ('replica.restore',   'Restore a replica from snapshot'),
    ('network.create',    'Create network'),
    ('network.read',      'Read network'),
    ('network.delete',    'Delete network'),
    ('certificate.read',  'Read certificates'),
    ('certificate.issue', 'Issue certificate'),
    ('event.read',        'Read events (durable spine)'),
    ('audit.read',        'Read audit log')
ON CONFLICT (id) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT v.role_id, v.permission_id FROM (VALUES
    ('member', 'replica.snapshot'), ('member', 'replica.restore'),
    ('member', 'network.create'), ('member', 'network.read'),
    ('member', 'event.read'),
    ('admin', 'replica.snapshot'), ('admin', 'replica.restore'),
    ('admin', 'network.create'), ('admin', 'network.read'), ('admin', 'network.delete'),
    ('admin', 'certificate.read'), ('admin', 'certificate.issue'),
    ('admin', 'event.read'), ('admin', 'audit.read'),
    ('owner', 'replica.snapshot'), ('owner', 'replica.restore'),
    ('owner', 'network.create'), ('owner', 'network.read'), ('owner', 'network.delete'),
    ('owner', 'certificate.read'), ('owner', 'certificate.issue'),
    ('owner', 'event.read'), ('owner', 'audit.read')
) AS v(role_id, permission_id)
WHERE NOT EXISTS (
    SELECT 1 FROM role_permissions rp WHERE rp.role_id = v.role_id AND rp.permission_id = v.permission_id
);
