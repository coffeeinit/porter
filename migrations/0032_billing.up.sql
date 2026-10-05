-- 0032_billing: plans + subscriptions + price book. Rating is computed
-- (usage_events × prices), money movement stays outside Porter — invoices
-- are previews until a payment provider adapter lands. Additive only.
CREATE TABLE IF NOT EXISTS billing_plans (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL DEFAULT '',
  monthly_cents BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS billing_prices (
  plan_id TEXT NOT NULL REFERENCES billing_plans(id) ON DELETE CASCADE,
  meter   TEXT NOT NULL,
  unit_cents DOUBLE PRECISION NOT NULL DEFAULT 0,
  unit    TEXT NOT NULL DEFAULT 'count',
  PRIMARY KEY (plan_id, meter)
);
CREATE TABLE IF NOT EXISTS billing_subscriptions (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id TEXT NOT NULL,
  plan_id    TEXT NOT NULL REFERENCES billing_plans(id),
  state      TEXT NOT NULL DEFAULT 'active',
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_billing_subs_project ON billing_subscriptions(project_id, state);
