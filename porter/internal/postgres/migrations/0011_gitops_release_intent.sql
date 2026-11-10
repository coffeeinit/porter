-- Porter PostgreSQL foundation, version 11. Explicit GitOps intent and durable, fail-closed request records.
-- Apply only through the explicit porter-migrate command.
ALTER TABLE porter_control_plane.releases
    ADD COLUMN deployment_intent TEXT NOT NULL DEFAULT 'preview'
        CHECK (deployment_intent IN ('preview', 'production'));
ALTER TABLE porter_control_plane.releases
    DROP CONSTRAINT IF EXISTS releases_state_check;
ALTER TABLE porter_control_plane.releases
    ADD CONSTRAINT releases_state_check
        CHECK (state IN ('queued', 'blocked', 'building', 'ready', 'deploying', 'active', 'failed', 'superseded'));
CREATE OR REPLACE FUNCTION porter_control_plane.guard_release_intent()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(NEW.id, NEW.service_id, NEW.sequence, NEW.source_kind, NEW.source_repository, NEW.source_ref, NEW.build_spec, NEW.deployment_intent)
       IS DISTINCT FROM
       ROW(OLD.id, OLD.service_id, OLD.sequence, OLD.source_kind, OLD.source_repository, OLD.source_ref, OLD.build_spec, OLD.deployment_intent) THEN
        RAISE EXCEPTION 'release source/build intent is immutable'
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.artifact_digest IS NOT NULL AND NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest THEN
        RAISE EXCEPTION 'release artifact digest is immutable once set'
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.resolved_commit IS NOT NULL AND NEW.resolved_commit IS DISTINCT FROM OLD.resolved_commit THEN
        RAISE EXCEPTION 'release resolved commit is immutable once set'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TABLE porter_control_plane.release_operations (
    id              TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    service_id      TEXT NOT NULL,
    release_id      TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('build', 'production_promotion')),
    state           TEXT NOT NULL CHECK (state = 'blocked'),
    intent          TEXT NOT NULL CHECK (intent IN ('preview', 'production')),
    blocker         TEXT NOT NULL CHECK (blocker ~ '^[a-z0-9_]{1,64}$'),
    requested_ref   TEXT NOT NULL CHECK (length(btrim(requested_ref)) > 0 AND octet_length(requested_ref) <= 255),
    resolved_commit TEXT CHECK (resolved_commit IS NULL OR resolved_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    checks          JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(checks) = 'array'),
    idempotency_key TEXT NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 128),
    request_sha256  TEXT NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (release_id, service_id)
        REFERENCES porter_control_plane.releases (id, service_id) ON DELETE RESTRICT,
    UNIQUE (service_id, idempotency_key)
);
CREATE INDEX release_operations_release_page_idx
    ON porter_control_plane.release_operations (release_id, created_at DESC, id DESC);
CREATE FUNCTION porter_control_plane.guard_release_operation_append_only()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'release operation records are append-only'
        USING ERRCODE = 'check_violation';
END;
$$;
CREATE TRIGGER release_operations_append_only
    BEFORE UPDATE OR DELETE ON porter_control_plane.release_operations
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_release_operation_append_only();
COMMENT ON COLUMN porter_control_plane.releases.deployment_intent IS
    'Immutable preview or production intent; it does not assert that a build or deployment ran.';
COMMENT ON TABLE porter_control_plane.release_operations IS
    'Append-only GitOps intent/status records. Blocked records do not imply build or production activation.';
