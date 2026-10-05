-- 0022_usage_events: write-only metering spine (commerce.md: shape now, money
-- later). No rating/invoicing tables here — rating ships with subscriptions.
-- Every meter rides this table; idempotency_key dedupes replays.
CREATE TABLE IF NOT EXISTS usage_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id TEXT NOT NULL,
  resource_ref TEXT NOT NULL DEFAULT '',
  meter TEXT NOT NULL,
  quantity DOUBLE PRECISION NOT NULL DEFAULT 0,
  unit TEXT NOT NULL DEFAULT 'count',
  idempotency_key TEXT NOT NULL DEFAULT '',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_events_idem ON usage_events(idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS idx_usage_events_project ON usage_events(project_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_events_meter ON usage_events(meter, occurred_at);
