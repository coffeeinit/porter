-- 0039_build_deployment_link: durable Git build/deployment relationship.
ALTER TABLE builds ADD COLUMN IF NOT EXISTS deployment_id UUID REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE builds ADD COLUMN IF NOT EXISTS git_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS build_id UUID REFERENCES builds(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_builds_deployment ON builds(deployment_id);
CREATE INDEX IF NOT EXISTS idx_deployments_build ON deployments(build_id);
