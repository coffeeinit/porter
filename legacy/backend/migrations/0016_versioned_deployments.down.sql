-- Rollback 0016_versioned_deployments.
DROP INDEX IF EXISTS idx_vms_deployment;
DROP INDEX IF EXISTS idx_deployments_project_production;
DROP INDEX IF EXISTS idx_deployments_project_created;
ALTER TABLE replicas DROP COLUMN IF EXISTS deployment_environment;
ALTER TABLE replicas DROP COLUMN IF EXISTS deployment_version;
ALTER TABLE replicas DROP COLUMN IF EXISTS deployment_id;
ALTER TABLE deployments DROP COLUMN IF EXISTS vm_ids;
ALTER TABLE deployments DROP COLUMN IF EXISTS guest_base;
ALTER TABLE deployments DROP COLUMN IF EXISTS route_weight;
ALTER TABLE deployments DROP COLUMN IF EXISTS is_production;
ALTER TABLE deployments DROP COLUMN IF EXISTS environment;
ALTER TABLE deployments DROP COLUMN IF EXISTS version_label;
