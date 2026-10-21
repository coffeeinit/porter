-- =============================================================
-- Deployment checks & rolling rollout.
-- A deployment can carry a list of required checks (build health, e2e status,
-- manual approval). Promotion is gated until every check passes, and rollout
-- supports gradual traffic weight (rolling releases).
-- =============================================================

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS checks JSONB NOT NULL DEFAULT '[]';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS rollout_percent INT NOT NULL DEFAULT 100;