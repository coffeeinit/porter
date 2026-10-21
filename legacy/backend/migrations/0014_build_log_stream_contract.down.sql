DROP INDEX IF EXISTS idx_build_logs_build_id;
ALTER TABLE build_logs DROP COLUMN IF EXISTS build_id;
