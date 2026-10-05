-- 0038_logs_trgm down: drop the trigram index only. The pg_trgm extension is
-- deliberately NOT dropped here — other objects may depend on it.
DROP INDEX IF EXISTS idx_vm_logs_message_trgm;
