-- 0033_preview_lease_seed: preview URLs routable, task leases, seed gap.
-- 1. deployments.preview_url persists the per-deployment preview host so
--    the gateway routes preview traffic to the right pool (not the pool).
-- 2. tasks.claimed_by/claimed_at lets multiple runners claim with
--    SELECT ... FOR UPDATE SKIP LOCKED instead of double-running.
-- 3. project.write was enforced by routes but never seeded (members 403).
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS preview_url TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS claimed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_tasks_claim ON tasks(status, claimed_at);

-- Seed the permission itself too: project.write was enforced by routes but
-- never seeded, so granting it on a fresh install violated the FK.
INSERT INTO permissions (id, name) VALUES ('project.write', 'Write projects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.role_id, 'project.write' FROM (VALUES ('admin'), ('member'), ('owner')) AS r(role_id)
JOIN roles ON roles.id = r.role_id
ON CONFLICT DO NOTHING;
