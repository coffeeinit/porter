-- Phase 3 rollback
-- 0020_golden_data.down.sql
ALTER TABLE golden_images DROP COLUMN IF EXISTS data;

DROP TABLE IF EXISTS traffic_logs;
DROP TABLE IF EXISTS vm_logs;
DROP TABLE IF EXISTS analytics_daily;
DROP TABLE IF EXISTS daemon_logs_history;
-- 0018_deploy_status_open.down.sql
ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_build_status_check;
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_replicas_desired_check;

ALTER TABLE deployments ADD CONSTRAINT deployments_build_status_check
    CHECK (build_status IN ('pending','building','ready','failed'));

ALTER TABLE projects ADD CONSTRAINT projects_replicas_desired_check
    CHECK (replicas_desired >= 1);
-- Rollback 0017.
DROP TABLE IF EXISTS daemon_logs;

-- Rollback 0016: remove the seeded golden images (leaves user uploads intact).
DELETE FROM golden_images WHERE name IN ('redis', 'postgresql', 'mysql');

-- Rollback 0015.
ALTER TABLE golden_images DROP COLUMN IF EXISTS data;

-- Rollback 0014 (re-add the legacy kind/status checks).
ALTER TABLE domains ADD CONSTRAINT domains_kind_check CHECK (kind IN ('stable','preview','custom'));
ALTER TABLE domains ADD CONSTRAINT domains_status_check CHECK (status IN ('pending','active','failed'));

