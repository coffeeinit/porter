-- Porter PostgreSQL foundation, version 5. Durable operations and fenced agent-command lease metadata.
-- Applied explicitly by the migration runner; no startup migration is performed.

ALTER TABLE porter_control_plane.services
    ADD CONSTRAINT services_id_project_unique UNIQUE (id, project_id);

ALTER TABLE porter_control_plane.resource_events
    ADD COLUMN operation_id TEXT,
    ADD COLUMN command_id TEXT;

CREATE INDEX resource_events_operation_page_idx
    ON porter_control_plane.resource_events (operation_id, occurred_at DESC, id DESC)
    WHERE operation_id IS NOT NULL;

CREATE INDEX resource_events_command_page_idx
    ON porter_control_plane.resource_events (command_id, occurred_at DESC, id DESC)
    WHERE command_id IS NOT NULL;

CREATE TABLE porter_control_plane.operations (
    id TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN (
        'vm.apply', 'vm.delete', 'vm.start', 'vm.stop', 'vm.restart',
        'snapshot.create', 'snapshot.restore', 'volume.resize'
    )),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    idempotency_key TEXT NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 128),
    request_sha256 TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (service_id, project_id)
        REFERENCES porter_control_plane.services (id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (instance_id, service_id)
        REFERENCES porter_control_plane.instances (id, service_id) ON DELETE RESTRICT,
    UNIQUE (id, project_id, service_id, instance_id),
    UNIQUE (project_id, idempotency_key)
);

CREATE INDEX operations_page_idx
    ON porter_control_plane.operations (created_at DESC, id DESC);

CREATE INDEX operations_instance_page_idx
    ON porter_control_plane.operations (instance_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX operations_one_active_per_instance_idx
    ON porter_control_plane.operations (instance_id)
    WHERE state IN ('queued', 'running');

CREATE TABLE porter_control_plane.agent_commands (
    id TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    operation_id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    node_id TEXT NOT NULL REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN (
        'vm.apply', 'vm.delete', 'vm.start', 'vm.stop', 'vm.restart',
        'snapshot.create', 'snapshot.restore', 'volume.resize'
    )),
    payload_version SMALLINT NOT NULL DEFAULT 1 CHECK (payload_version = 1),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'leased', 'succeeded', 'failed')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_token_hash TEXT CHECK (lease_token_hash IS NULL OR lease_token_hash ~ '^[0-9a-f]{64}$'),
    lease_expires_at TIMESTAMPTZ,
    result_received_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (operation_id, project_id, service_id, instance_id)
        REFERENCES porter_control_plane.operations (id, project_id, service_id, instance_id) ON DELETE RESTRICT,
    UNIQUE (id, operation_id),
    CHECK (
        (state = 'queued' AND lease_token_hash IS NULL AND lease_expires_at IS NULL AND result_received_at IS NULL)
        OR (state = 'leased' AND attempt > 0 AND lease_token_hash IS NOT NULL AND lease_expires_at IS NOT NULL AND result_received_at IS NULL)
        OR (state IN ('succeeded', 'failed') AND attempt > 0 AND lease_token_hash IS NOT NULL AND lease_expires_at IS NOT NULL AND result_received_at IS NOT NULL)
    ),
    CHECK (
        (kind IN ('vm.apply', 'vm.start', 'vm.stop', 'vm.restart')
            AND length(btrim(COALESCE(payload->>'vm_id', ''))) > 0)
        OR (kind = 'vm.delete' AND length(btrim(COALESCE(payload->>'vm_id', ''))) > 0)
        OR (kind = 'snapshot.create'
            AND length(btrim(COALESCE(payload->>'vm_id', ''))) > 0
            AND length(btrim(COALESCE(payload->>'snapshot_id', ''))) > 0)
        OR (kind = 'snapshot.restore'
            AND length(btrim(COALESCE(payload->>'vm_id', ''))) > 0
            AND length(btrim(COALESCE(payload->>'snapshot_id', ''))) > 0)
        OR (kind = 'volume.resize'
            AND length(btrim(COALESCE(payload->>'volume_id', ''))) > 0
            AND COALESCE((payload->>'size_mb')::INTEGER, 0) > 0)
    )
);

CREATE INDEX agent_commands_node_queue_idx
    ON porter_control_plane.agent_commands (node_id, created_at, id)
    WHERE state = 'queued';

CREATE INDEX agent_commands_lease_expiry_idx
    ON porter_control_plane.agent_commands (lease_expires_at, id)
    WHERE state = 'leased';

CREATE TABLE porter_control_plane.agent_command_results (
    id TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    command_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    node_id TEXT NOT NULL REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    lease_token_hash TEXT NOT NULL CHECK (lease_token_hash ~ '^[0-9a-f]{64}$'),
    success BOOLEAN NOT NULL,
    result_state TEXT NOT NULL CHECK (result_state IN ('running', 'stopped', 'deleted', 'snapshot_ready', 'restored', 'resized', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (command_id, operation_id)
        REFERENCES porter_control_plane.agent_commands (id, operation_id) ON DELETE RESTRICT,
    FOREIGN KEY (operation_id, project_id, service_id, instance_id)
        REFERENCES porter_control_plane.operations (id, project_id, service_id, instance_id) ON DELETE RESTRICT,
    UNIQUE (command_id, attempt, lease_token_hash),
    CHECK ((success AND result_state <> 'failed' AND error = '') OR (NOT success AND result_state = 'failed' AND length(btrim(error)) > 0))
);

CREATE INDEX agent_command_results_operation_idx
    ON porter_control_plane.agent_command_results (operation_id, received_at DESC, id DESC);

CREATE FUNCTION porter_control_plane.reject_agent_command_result_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent command results are append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER agent_command_results_append_only
BEFORE UPDATE OR DELETE ON porter_control_plane.agent_command_results
FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_agent_command_result_mutation();

COMMENT ON TABLE porter_control_plane.operations IS
    'Project/Service/Instance-scoped durable operation intent; no operation is executed by the API.';
COMMENT ON TABLE porter_control_plane.agent_commands IS
    'Allowlisted typed command metadata with expiring hashed leases; no agent polling route is enabled.';
COMMENT ON TABLE porter_control_plane.agent_command_results IS
    'Append-only, attempt- and lease-fenced agent result receipts.';
