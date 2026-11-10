-- Porter PostgreSQL foundation, version 7. Short-lived, one-time operator-issued agent enrollment.
-- Applied explicitly by the migration runner; no startup migration is performed.

ALTER TABLE porter_control_plane.fleet_nodes
    ADD COLUMN enrollment_target TEXT NOT NULL DEFAULT 'unspecified'
        CHECK (enrollment_target IN ('unspecified', 'same_host', 'remote_host'));

CREATE TABLE porter_control_plane.agent_enrollment_tokens (
    id TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    enrollment_target TEXT NOT NULL CHECK (enrollment_target IN ('same_host', 'remote_host')),
    control_plane_url TEXT NOT NULL CHECK (control_plane_url ~ '^https://[^/?#]+$'),
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    fleet_node_id TEXT UNIQUE REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '30 minutes'),
    CHECK ((consumed_at IS NULL) = (fleet_node_id IS NULL)),
    CHECK (consumed_at IS NULL OR (consumed_at >= issued_at AND consumed_at <= expires_at))
);

CREATE INDEX agent_enrollment_tokens_expiry_idx
    ON porter_control_plane.agent_enrollment_tokens (expires_at, id)
    WHERE consumed_at IS NULL;

CREATE FUNCTION porter_control_plane.guard_agent_enrollment_token_update()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent enrollment tokens are retained for audit'
            USING ERRCODE = 'check_violation';
    END IF;

    IF ROW(NEW.id, NEW.token_hash, NEW.enrollment_target, NEW.control_plane_url, NEW.issued_at, NEW.expires_at)
       IS DISTINCT FROM
       ROW(OLD.id, OLD.token_hash, OLD.enrollment_target, OLD.control_plane_url, OLD.issued_at, OLD.expires_at) THEN
        RAISE EXCEPTION 'agent enrollment token intent is immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.consumed_at IS NOT NULL OR OLD.fleet_node_id IS NOT NULL
       OR NEW.consumed_at IS NULL OR NEW.fleet_node_id IS NULL THEN
        RAISE EXCEPTION 'agent enrollment token may be consumed once only'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_enrollment_tokens_single_use
    BEFORE UPDATE OR DELETE ON porter_control_plane.agent_enrollment_tokens
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_agent_enrollment_token_update();

COMMENT ON TABLE porter_control_plane.agent_enrollment_tokens IS
    'Short-lived, single-use agent enrollment credentials stored only as SHA-256 hashes; successful redemption binds the token to a server-created Fleet node.';
