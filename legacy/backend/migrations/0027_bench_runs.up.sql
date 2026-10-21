-- 0027_bench_runs: persisted benchmark summaries (PVE-09) for SLO
-- dashboards and placement-weight tuning. Write-only telemetry; budgets
-- live in code/ops config, never here.
CREATE TABLE IF NOT EXISTS bench_runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  memory_mib INT NOT NULL DEFAULT 0,
  serial_ms BIGINT NOT NULL DEFAULT 0,
  shell_ms BIGINT NOT NULL DEFAULT 0,
  agent_ms BIGINT NOT NULL DEFAULT 0,
  rss_mib BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_bench_runs_name ON bench_runs(name, created_at DESC);
