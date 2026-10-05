-- 0036_vps_workload_type rollback.
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_workload_type_check;
ALTER TABLE projects DROP COLUMN IF EXISTS workload_type;
ALTER TABLE projects DROP COLUMN IF EXISTS persistent;
ALTER TABLE ip_allocations DROP COLUMN IF EXISTS pinned;
