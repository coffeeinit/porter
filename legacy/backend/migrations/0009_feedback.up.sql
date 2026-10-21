-- =============================================================
-- User feedback submissions.
-- A lightweight, single-tenant feedback channel: the dashboard "Send
-- feedback" form posts here; rows are durable in Postgres and surfaced
-- to the operator via GET /feedback (and on the /logs daemon view).
-- =============================================================

CREATE TABLE IF NOT EXISTS feedback (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject     TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL,
    category    TEXT NOT NULL DEFAULT 'general',
    username    TEXT NOT NULL DEFAULT 'admin',
    project_id  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
