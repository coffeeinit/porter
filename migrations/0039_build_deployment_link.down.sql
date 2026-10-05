DROP INDEX IF EXISTS idx_deployments_build;
DROP INDEX IF EXISTS idx_builds_deployment;
ALTER TABLE deployments DROP COLUMN IF EXISTS build_id;
ALTER TABLE builds DROP COLUMN IF EXISTS git_commit;
ALTER TABLE builds DROP COLUMN IF EXISTS deployment_id;
