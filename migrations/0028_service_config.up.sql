-- 0028_service_config: durable service config truth for the config-push
-- applier. One row per service: optimistic-concurrency revision + env map.
-- The applier CAS-swaps on base_rev so concurrent pushes never silently
-- overwrite each other; guest boot renders the managed env block from here.
CREATE TABLE IF NOT EXISTS service_config (
  service_id TEXT PRIMARY KEY,
  rev        TEXT NOT NULL DEFAULT '',
  env        JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
