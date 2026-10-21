-- Phase 2 rollback
-- Rollback 0013 (re-add NOT NULL; only safe when no rows lack these values).
ALTER TABLE projects ALTER COLUMN bridge_subnet SET NOT NULL;
ALTER TABLE replicas ALTER COLUMN service_id SET NOT NULL;
ALTER TABLE domains ALTER COLUMN service_id SET NOT NULL;

-- Rollback 0012.
ALTER TABLE orgs DROP COLUMN IF EXISTS owner_id;
ALTER TABLE orgs DROP COLUMN IF EXISTS is_default;
ALTER TABLE health_events DROP COLUMN IF EXISTS project_id;

-- 0011_full_paas_schema: rollback tables/columns added in the .up migration.

ALTER TABLE projects DROP COLUMN IF EXISTS stack_id;
ALTER TABLE projects DROP COLUMN IF EXISTS source;
ALTER TABLE projects DROP COLUMN IF EXISTS model;
ALTER TABLE projects DROP COLUMN IF EXISTS gpu;
ALTER TABLE projects DROP COLUMN IF EXISTS compose_service;
ALTER TABLE projects DROP COLUMN IF EXISTS networks;
ALTER TABLE replicas DROP COLUMN IF EXISTS console_port;
ALTER TABLE replicas DROP COLUMN IF EXISTS ssh_enabled;

DROP TABLE IF EXISTS networks;
DROP TABLE IF EXISTS builds;
DROP TABLE IF EXISTS project_members;
DROP TABLE IF EXISTS project_settings;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS firewall_rules;
DROP TABLE IF EXISTS redirects;
DROP TABLE IF EXISTS drains;
DROP TABLE IF EXISTS crons;
DROP TABLE IF EXISTS hooks;
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS volumes;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS stacks;
-- 0010_v3_full_schema.down.sql — removes v3 schema objects. 0011 depends on
-- pieces of 0010, so this is best-effort for a fresh-rollback scenario only.
ALTER TABLE projects DROP COLUMN IF EXISTS env;
ALTER TABLE projects DROP COLUMN IF EXISTS healthcheck;
ALTER TABLE projects DROP COLUMN IF EXISTS restart_policy;
ALTER TABLE projects DROP COLUMN IF EXISTS replicas_desired;
ALTER TABLE projects DROP COLUMN IF EXISTS host_mount_path;
ALTER TABLE projects DROP COLUMN IF EXISTS image;
ALTER TABLE projects DROP COLUMN IF EXISTS org_id;

DROP TABLE IF EXISTS group_projects;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS orgs;
DROP TABLE IF EXISTS build_logs;
DROP TABLE IF EXISTS dns_records;
ALTER TABLE projects DROP COLUMN IF EXISTS tags;
ALTER TABLE projects DROP COLUMN IF EXISTS org_id;
DROP TABLE IF EXISTS orgs;

