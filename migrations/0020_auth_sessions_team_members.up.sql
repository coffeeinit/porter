-- =============================================================
-- 0020_auth_sessions_team_members: DB sessions (T3) + team membership (T5).
--   auth_sessions — opaque session tokens (hash-stored, revocable, expiring).
--   team_members  — per-team (groups.id) membership with role. GroupRoleForUser
--                   checks this table first, then falls back to the owning
--                   org's membership (documented inheritance).
-- =============================================================

CREATE TABLE IF NOT EXISTS auth_sessions (
    id             TEXT PRIMARY KEY,
    principal_type TEXT NOT NULL DEFAULT 'user',
    principal_id   TEXT NOT NULL,
    token_hash     TEXT NOT NULL UNIQUE,
    scope_type     TEXT NOT NULL DEFAULT 'platform',
    scope_id       TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_principal ON auth_sessions(principal_type, principal_id, expires_at);

CREATE TABLE IF NOT EXISTS team_members (
    group_id   UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
