-- 0026_feature_tables rollback (reverse creation order for FK safety).
DROP TABLE IF EXISTS node_heartbeats;
DROP TABLE IF EXISTS enrollment_tokens;
DROP TABLE IF EXISTS marketplace_items;
DROP TABLE IF EXISTS workflow_events;
DROP TABLE IF EXISTS workflow_runs;
DROP TABLE IF EXISTS backups;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS capacity_policies;
DROP TABLE IF EXISTS placements;
