-- 0033_preview_lease_seed rollback.
DELETE FROM role_permissions WHERE permission_id = 'project.write';
DROP INDEX IF EXISTS idx_tasks_claim;
ALTER TABLE tasks DROP COLUMN IF EXISTS claimed_at;
ALTER TABLE tasks DROP COLUMN IF EXISTS claimed_by;
ALTER TABLE deployments DROP COLUMN IF EXISTS preview_url;
