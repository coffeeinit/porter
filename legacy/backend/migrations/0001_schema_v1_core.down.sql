-- Phase 1 rollback (reverse order)
DROP TABLE IF EXISTS health_events;

DROP TABLE IF EXISTS metrics_samples;

DROP TABLE IF EXISTS golden_images;

DROP TABLE IF EXISTS networks;

DROP TABLE IF EXISTS deployments;

DROP TABLE IF EXISTS secrets;

DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS users;
DROP INDEX IF EXISTS idx_services_project_id;
DROP INDEX IF EXISTS idx_domains_vm_id;
DROP INDEX IF EXISTS idx_vms_data_state;
ALTER TABLE servers  DROP COLUMN IF EXISTS data;
ALTER TABLE volumes  DROP COLUMN IF EXISTS data;
ALTER TABLE domains  DROP COLUMN IF EXISTS data;
ALTER TABLE domains  DROP COLUMN IF EXISTS vm_id;
ALTER TABLE vms      DROP COLUMN IF EXISTS data;
ALTER TABLE projects DROP COLUMN IF EXISTS data;

-- 0001_init: rollback (Section 4.3 — never hand-edit a merged migration; this
-- is the paired down file for local reset only).
DROP TABLE IF EXISTS servers;
DROP TABLE IF EXISTS ssh_keys;
DROP TABLE IF EXISTS volumes;
DROP TABLE IF EXISTS domains;
DROP TABLE IF EXISTS vms;
DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS projects;
DROP EXTENSION IF EXISTS pgcrypto;

