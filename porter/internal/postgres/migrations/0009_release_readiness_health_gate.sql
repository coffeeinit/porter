-- Porter PostgreSQL foundation, version 9. Guest readiness and health-gated activation.
-- Apply only through the explicit porter-migrate command.

ALTER TABLE porter_control_plane.releases
    ADD COLUMN guest_ready_digest TEXT
        CHECK (guest_ready_digest IS NULL OR guest_ready_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN guest_ready_at TIMESTAMPTZ,
    ADD COLUMN health_gate_status TEXT NOT NULL DEFAULT 'not_run'
        CHECK (health_gate_status IN ('not_run', 'pending', 'passed', 'failed')),
    ADD COLUMN health_gate_digest TEXT
        CHECK (health_gate_digest IS NULL OR health_gate_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN health_gate_checked_at TIMESTAMPTZ,
    ADD COLUMN health_gate_error TEXT NOT NULL DEFAULT ''
        CHECK (octet_length(health_gate_error) <= 1024),
    ADD CONSTRAINT releases_guest_ready_consistency CHECK (
        (guest_ready_digest IS NULL AND guest_ready_at IS NULL)
        OR (guest_ready_digest = artifact_digest AND guest_ready_at IS NOT NULL)
    ),
    ADD CONSTRAINT releases_health_gate_consistency CHECK (
        (health_gate_status IN ('not_run', 'pending')
            AND health_gate_digest IS NULL
            AND health_gate_checked_at IS NULL
            AND health_gate_error = '')
        OR (health_gate_status = 'passed'
            AND health_gate_digest = artifact_digest
            AND health_gate_checked_at IS NOT NULL
            AND health_gate_error = '')
        OR (health_gate_status = 'failed'
            AND health_gate_digest = artifact_digest
            AND health_gate_checked_at IS NOT NULL
            AND health_gate_error <> '')
    );

CREATE FUNCTION porter_control_plane.guard_release_readiness()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.guest_ready_digest IS NOT NULL
       AND (NEW.guest_ready_digest IS DISTINCT FROM OLD.guest_ready_digest
            OR NEW.guest_ready_at IS DISTINCT FROM OLD.guest_ready_at) THEN
        RAISE EXCEPTION 'guest-ready artifact evidence is immutable once set'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.health_gate_status IN ('passed', 'failed')
       AND (NEW.health_gate_status IS DISTINCT FROM OLD.health_gate_status
            OR NEW.health_gate_digest IS DISTINCT FROM OLD.health_gate_digest
            OR NEW.health_gate_checked_at IS DISTINCT FROM OLD.health_gate_checked_at
            OR NEW.health_gate_error IS DISTINCT FROM OLD.health_gate_error) THEN
        RAISE EXCEPTION 'completed release health-gate evidence is immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.health_gate_status = 'pending' AND NEW.state <> 'deploying' THEN
        RAISE EXCEPTION 'release health gate can be pending only during deployment'
            USING ERRCODE = 'check_violation';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state THEN
        IF NEW.state IN ('ready', 'deploying', 'superseded')
           AND (OLD.guest_ready_digest IS NULL
                OR OLD.guest_ready_digest IS DISTINCT FROM OLD.artifact_digest
                OR OLD.guest_ready_at IS NULL
                OR NEW.guest_ready_digest IS NULL
                OR NEW.guest_ready_digest IS DISTINCT FROM NEW.artifact_digest
                OR NEW.guest_ready_at IS NULL) THEN
            RAISE EXCEPTION 'release artifact must be guest-ready before ready, deploying, or superseded'
                USING ERRCODE = 'check_violation';
        END IF;
        IF NEW.state IN ('active', 'superseded')
           AND (OLD.guest_ready_digest IS NULL
                OR OLD.guest_ready_digest IS DISTINCT FROM OLD.artifact_digest
                OR OLD.health_gate_status <> 'passed'
                OR OLD.health_gate_digest IS DISTINCT FROM OLD.artifact_digest
                OR OLD.health_gate_checked_at IS NULL
                OR NEW.guest_ready_digest IS NULL
                OR NEW.guest_ready_digest IS DISTINCT FROM NEW.artifact_digest
                OR NEW.health_gate_status <> 'passed'
                OR NEW.health_gate_digest IS DISTINCT FROM NEW.artifact_digest
                OR NEW.health_gate_checked_at IS NULL) THEN
            RAISE EXCEPTION 'release must be guest-ready and pass its health gate before activation'
                USING ERRCODE = 'check_violation';
        END IF;
    END IF;

    IF OLD.health_gate_status = 'not_run' AND NEW.health_gate_status NOT IN ('not_run', 'pending') THEN
        RAISE EXCEPTION 'release health gate must enter pending before completion'
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.health_gate_status = 'pending'
       AND NEW.health_gate_status NOT IN ('pending', 'passed', 'failed') THEN
        RAISE EXCEPTION 'release health gate transition is invalid'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER releases_readiness_guard
    BEFORE UPDATE ON porter_control_plane.releases
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_release_readiness();

COMMENT ON COLUMN porter_control_plane.releases.guest_ready_digest IS
    'Digest of the artifact proven bootable by the agent local Firecracker runtime; no local path is stored.';
COMMENT ON COLUMN porter_control_plane.releases.health_gate_status IS
    'Deployment health-gate state; activation is rejected until the same guest-ready digest passes.';
