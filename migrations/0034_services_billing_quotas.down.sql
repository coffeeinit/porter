-- 0034_services_billing_quotas rollback.
DROP INDEX IF EXISTS idx_backup_sched_unique;
DROP TABLE IF EXISTS plan_limits;
DROP TABLE IF EXISTS backup_schedules;
DROP TABLE IF EXISTS service_templates;
