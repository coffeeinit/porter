-- Porter PostgreSQL foundation, version 12. Typed builder receipts are kept
-- separate from Firecracker guest-ready artifact evidence and are append-only.
-- Apply only through the explicit porter-migrate command.
ALTER TABLE porter_control_plane.release_operations
    ADD COLUMN builder_spec JSONB
        CHECK (builder_spec IS NULL OR jsonb_typeof(builder_spec) = 'object');
ALTER TABLE porter_control_plane.release_operations
    DROP CONSTRAINT IF EXISTS release_operations_state_check;
ALTER TABLE porter_control_plane.release_operations
    ADD CONSTRAINT release_operations_state_check CHECK (
        (state = 'blocked' AND builder_spec IS NULL)
        OR (state = 'running' AND kind = 'build' AND builder_spec IS NOT NULL
            AND resolved_commit IS NOT NULL
            AND resolved_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND builder_spec->>'commit' IS NOT NULL
            AND builder_spec->>'commit' = resolved_commit
            AND builder_spec->>'repository_url' LIKE 'https://%'
            AND blocker = 'builder_running')
    );
ALTER TABLE porter_control_plane.release_operations
    ADD CONSTRAINT release_operations_builder_identity_unique UNIQUE (id, service_id, release_id);

CREATE TABLE porter_control_plane.release_builder_results (
    id              TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    command_id      TEXT NOT NULL UNIQUE CHECK (length(btrim(command_id)) BETWEEN 1 AND 128),
    operation_id    TEXT NOT NULL,
    service_id      TEXT NOT NULL,
    release_id      TEXT NOT NULL,
    node_id         TEXT NOT NULL CHECK (length(btrim(node_id)) BETWEEN 1 AND 128),
    attempt         INTEGER NOT NULL CHECK (attempt > 0),
    builder_spec    JSONB NOT NULL CHECK (jsonb_typeof(builder_spec) = 'object'),
    command_result  JSONB NOT NULL CHECK (jsonb_typeof(command_result) = 'object'),
    success         BOOLEAN NOT NULL,
    error           TEXT NOT NULL DEFAULT '' CHECK (octet_length(error) <= 4096),
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (operation_id, service_id, release_id)
        REFERENCES porter_control_plane.release_operations (id, service_id, release_id) ON DELETE RESTRICT,
    UNIQUE (operation_id, attempt),
    CHECK (command_result->>'operation_id' = operation_id),
    CHECK (command_result->>'command_id' = command_id),
    CHECK (command_result->>'node_id' = node_id),
    CHECK (command_result->>'attempt' = attempt::text),
    CHECK (command_result->>'success' = success::text),
    CHECK (COALESCE(command_result->>'error', '') = error),
    CHECK (
        (success
            AND error = ''
            AND command_result->>'state' = 'built'
            AND jsonb_typeof(command_result->'builder_result') = 'object'
            AND command_result->'builder_result'->>'build_id' = builder_spec->>'build_id'
            AND command_result->'builder_result'->>'architecture' = builder_spec->>'architecture'
            AND command_result->'builder_result'->>'digest' ~ '^sha256:[0-9a-f]{64}$'
            AND position('@' || (command_result->'builder_result'->>'digest') IN (command_result->'builder_result'->>'oci_reference')) > 0
            AND command_result->'builder_result'->>'size_bytes' ~ '^[1-9][0-9]*$')
        OR
        (NOT success
            AND length(btrim(error)) > 0
            AND command_result->'builder_result' IS NULL)
    ),
    CHECK (builder_spec->>'commit' IS NOT NULL AND builder_spec->>'commit' ~ '^([0-9a-f]{40}|[0-9a-f]{64})$')
);
CREATE INDEX release_builder_results_operation_idx
    ON porter_control_plane.release_builder_results (operation_id, attempt DESC, received_at DESC);

CREATE FUNCTION porter_control_plane.guard_release_builder_result()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    operation_kind TEXT;
    operation_state TEXT;
    operation_service_id TEXT;
    operation_release_id TEXT;
    operation_commit TEXT;
    operation_spec JSONB;
    source_repository TEXT;
    release_commit TEXT;
    release_state TEXT;
BEGIN
    SELECT o.kind, o.state, o.service_id, o.release_id, o.resolved_commit, o.builder_spec
      INTO operation_kind, operation_state, operation_service_id, operation_release_id, operation_commit, operation_spec
      FROM porter_control_plane.release_operations o
     WHERE o.id = NEW.operation_id
     FOR UPDATE;
    IF NOT FOUND OR operation_kind <> 'build' OR operation_state <> 'running'
       OR operation_service_id <> NEW.service_id OR operation_release_id <> NEW.release_id
       OR operation_commit IS DISTINCT FROM NEW.builder_spec->>'commit'
       OR operation_spec IS DISTINCT FROM NEW.builder_spec THEN
        RAISE EXCEPTION 'builder receipt does not match a running pinned build operation'
            USING ERRCODE = 'check_violation';
    END IF;

    SELECT r.source_repository, r.resolved_commit, r.state
      INTO source_repository, release_commit, release_state
      FROM porter_control_plane.releases r
     WHERE r.id = NEW.release_id AND r.service_id = NEW.service_id;
    IF NOT FOUND OR release_state <> 'building'
       OR source_repository IS DISTINCT FROM NEW.builder_spec->>'repository_url'
       OR release_commit IS DISTINCT FROM NEW.builder_spec->>'commit' THEN
        RAISE EXCEPTION 'builder receipt does not match the pinned source release'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER release_builder_results_match_pinned_operation
    BEFORE INSERT ON porter_control_plane.release_builder_results
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_release_builder_result();

CREATE FUNCTION porter_control_plane.reject_release_builder_result_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'release builder results are append-only'
        USING ERRCODE = 'check_violation';
END;
$$;
CREATE TRIGGER release_builder_results_append_only
    BEFORE UPDATE OR DELETE ON porter_control_plane.release_builder_results
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_release_builder_result_mutation();

COMMENT ON COLUMN porter_control_plane.release_operations.builder_spec IS
    'Validated, fully pinned builder.build request; absent from blocked intents until a trusted builder is available.';
COMMENT ON TABLE porter_control_plane.release_builder_results IS
    'Append-only typed builder command receipts. An OCI result is not Firecracker guest-ready or production evidence.';
