-- Porter PostgreSQL foundation, version 10. Agent-reported logs and metrics.
-- Retention and per-node row quotas are enforced by the store ingest path; query
-- paths also exclude expired records. No traces or arbitrary attributes are stored.

CREATE TABLE porter_control_plane.telemetry_logs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fleet_node_id TEXT NOT NULL REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE CASCADE,
    record_id TEXT NOT NULL CHECK (record_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    instance_id TEXT REFERENCES porter_control_plane.instances(id) ON DELETE SET NULL,
    project_id TEXT,
    service_id TEXT,
    reported_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    level TEXT NOT NULL CHECK (level IN ('debug', 'info', 'warn', 'error')),
    message TEXT NOT NULL CHECK (octet_length(message) BETWEEN 1 AND 4096),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    CHECK (expires_at > received_at AND expires_at <= received_at + interval '30 days'),
    UNIQUE (fleet_node_id, record_id)
);

CREATE TABLE porter_control_plane.telemetry_metrics (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fleet_node_id TEXT NOT NULL REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE CASCADE,
    record_id TEXT NOT NULL CHECK (record_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    instance_id TEXT REFERENCES porter_control_plane.instances(id) ON DELETE SET NULL,
    project_id TEXT,
    service_id TEXT,
    reported_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    metric_name TEXT NOT NULL CHECK (metric_name ~ '^[A-Za-z][A-Za-z0-9_.:-]{0,127}$'),
    metric_value DOUBLE PRECISION NOT NULL CHECK (metric_value > '-Infinity'::float8 AND metric_value < 'Infinity'::float8),
    unit TEXT NOT NULL DEFAULT '' CHECK (octet_length(unit) <= 32 AND (unit = '' OR unit ~ '^[A-Za-z][A-Za-z0-9./_%:-]{0,31}$')),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    CHECK (expires_at > received_at AND expires_at <= received_at + interval '30 days'),
    UNIQUE (fleet_node_id, record_id)
);

CREATE INDEX telemetry_logs_global_time_idx
    ON porter_control_plane.telemetry_logs (reported_at DESC, id DESC);
CREATE INDEX telemetry_logs_node_time_idx
    ON porter_control_plane.telemetry_logs (fleet_node_id, reported_at DESC, id DESC);
CREATE INDEX telemetry_logs_instance_time_idx
    ON porter_control_plane.telemetry_logs (instance_id, reported_at DESC, id DESC)
    WHERE instance_id IS NOT NULL;
CREATE INDEX telemetry_logs_retention_idx
    ON porter_control_plane.telemetry_logs (expires_at, fleet_node_id);

CREATE INDEX telemetry_metrics_global_time_idx
    ON porter_control_plane.telemetry_metrics (reported_at DESC, id DESC);
CREATE INDEX telemetry_metrics_node_time_idx
    ON porter_control_plane.telemetry_metrics (fleet_node_id, reported_at DESC, id DESC);
CREATE INDEX telemetry_metrics_instance_time_idx
    ON porter_control_plane.telemetry_metrics (instance_id, reported_at DESC, id DESC)
    WHERE instance_id IS NOT NULL;
CREATE INDEX telemetry_metrics_retention_idx
    ON porter_control_plane.telemetry_metrics (expires_at, fleet_node_id);

COMMENT ON TABLE porter_control_plane.telemetry_logs IS
    'Redacted, bounded agent/runtime log records. Does not contain application stdout/stderr or distributed spans.';
COMMENT ON TABLE porter_control_plane.telemetry_metrics IS
    'Bounded numeric node/Firecracker-process measurements reported by authenticated v2 agents.';
