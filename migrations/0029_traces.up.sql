-- 0029_traces: durable AI-ops traces (OCM-24). Append-only trace/span
-- rows plus human/judge feedback scores. Transport stays OTel-compatible;
-- these tables are the queryable history behind the live exporters.
CREATE TABLE IF NOT EXISTS traces (
  id         TEXT PRIMARY KEY,
  service    TEXT NOT NULL DEFAULT '',
  operation  TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS spans (
  trace_id    TEXT NOT NULL REFERENCES traces(id) ON DELETE CASCADE,
  span_id     TEXT NOT NULL,
  parent_id   TEXT NOT NULL DEFAULT '',
  name        TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  error       TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (trace_id, span_id)
);
CREATE TABLE IF NOT EXISTS feedback_scores (
  trace_id   TEXT NOT NULL REFERENCES traces(id) ON DELETE CASCADE,
  name       TEXT NOT NULL DEFAULT '',
  value      DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
