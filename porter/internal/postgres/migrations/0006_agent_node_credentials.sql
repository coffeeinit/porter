-- Porter PostgreSQL foundation, version 6. Hashed, rotation-safe node credentials for opt-in v2 agent transport.
-- Applied explicitly by the migration runner; no startup migration is performed.

CREATE TABLE porter_control_plane.agent_node_credentials (
    node_id TEXT NOT NULL REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL CHECK (generation > 0),
    token_hash BYTEA NOT NULL CHECK (octet_length(token_hash) = 32),
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    PRIMARY KEY (node_id, generation),
    UNIQUE (token_hash),
    CHECK (expires_at IS NULL OR expires_at >= issued_at),
    CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);

CREATE UNIQUE INDEX agent_node_credentials_one_current_idx
    ON porter_control_plane.agent_node_credentials (node_id)
    WHERE expires_at IS NULL AND revoked_at IS NULL;

CREATE INDEX agent_node_credentials_auth_lookup_idx
    ON porter_control_plane.agent_node_credentials (node_id, expires_at)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE porter_control_plane.agent_node_credentials IS
    'High-entropy node bearer credentials stored only as SHA-256 hashes; rotated credentials remain valid only through their bounded grace expiry.';
