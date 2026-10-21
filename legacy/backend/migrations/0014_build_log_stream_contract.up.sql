-- Build-scoped log identity for live build/deploy streams. Existing rows remain
-- valid as project-level history with a NULL build_id.
ALTER TABLE build_logs
    ADD COLUMN IF NOT EXISTS build_id UUID REFERENCES builds(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_build_logs_build_id ON build_logs(build_id, id);
