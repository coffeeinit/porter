-- 0038_logs_trgm: trigram index for the durable log search.
-- store.SearchVMLogs (GET /projects/{projectId}/replicas/{n}/logs/search)
-- runs a leading-wildcard ILIKE over vm_logs.line, which cannot use the
-- btree indexes; the pg_trgm GIN index gives it an index path instead of a
-- full scan of the durable log table.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_vm_logs_message_trgm ON vm_logs USING gin (line gin_trgm_ops);
