-- Porter PostgreSQL foundation, version 1.
-- Fresh-database foundation only; it is not wired to the active SQLite control plane.
-- Apply only through the explicit porter-migrate command, never during server startup.

CREATE SCHEMA porter_control_plane;

CREATE TABLE porter_control_plane.projects (
    id          TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    name        TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    state       TEXT NOT NULL DEFAULT 'active'
                    CHECK (state IN ('active', 'deleting')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX projects_page_idx
    ON porter_control_plane.projects (created_at, id);

CREATE TABLE porter_control_plane.fleet_nodes (
    id          TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    name        TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    state       TEXT NOT NULL DEFAULT 'offline'
                    CHECK (state IN ('ready', 'cordoned', 'draining', 'offline')),
    last_seen_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX fleet_nodes_state_idx
    ON porter_control_plane.fleet_nodes (state, id);

CREATE TABLE porter_control_plane.services (
    id                TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    project_id        TEXT NOT NULL REFERENCES porter_control_plane.projects(id) ON DELETE RESTRICT,
    name              TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    state             TEXT NOT NULL DEFAULT 'active'
                          CHECK (state IN ('active', 'deleting')),
    runtime           TEXT NOT NULL DEFAULT 'firecracker'
                          CHECK (runtime = 'firecracker'),
    port              INTEGER NOT NULL DEFAULT 8080 CHECK (port BETWEEN 1 AND 65535),
    vcpus             INTEGER NOT NULL DEFAULT 1 CHECK (vcpus > 0),
    memory_mb         INTEGER NOT NULL DEFAULT 512 CHECK (memory_mb > 0),
    desired_instances INTEGER NOT NULL DEFAULT 1 CHECK (desired_instances >= 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX services_project_page_idx
    ON porter_control_plane.services (project_id, created_at, id);

CREATE TABLE porter_control_plane.releases (
    id               TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    service_id       TEXT NOT NULL REFERENCES porter_control_plane.services(id) ON DELETE RESTRICT,
    sequence         BIGINT NOT NULL CHECK (sequence > 0),
    state            TEXT NOT NULL DEFAULT 'queued'
                         CHECK (state IN ('queued', 'building', 'ready', 'deploying', 'active', 'failed', 'superseded')),
    source_kind      TEXT NOT NULL DEFAULT 'git' CHECK (source_kind = 'git'),
    source_repository TEXT NOT NULL CHECK (length(btrim(source_repository)) > 0),
    source_ref       TEXT NOT NULL CHECK (length(btrim(source_ref)) > 0),
    resolved_commit  TEXT CHECK (resolved_commit IS NULL OR resolved_commit ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    build_spec       JSONB NOT NULL
                         CHECK (jsonb_typeof(build_spec) = 'object')
                         CHECK (build_spec ? 'kind' AND build_spec->>'kind' IN ('dockerfile', 'compose'))
                         CHECK (
                             build_spec->>'kind' <> 'dockerfile'
                             OR length(btrim(COALESCE(build_spec->>'dockerfile_path', ''))) > 0
                         )
                         CHECK (
                             build_spec->>'kind' <> 'compose'
                             OR (
                                 length(btrim(COALESCE(build_spec->>'compose_file', ''))) > 0
                                 AND length(btrim(COALESCE(build_spec->>'primary_service', ''))) > 0
                             )
                         ),
    artifact_digest  TEXT CHECK (artifact_digest IS NULL OR artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    failure_message  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, sequence),
    UNIQUE (id, service_id),
    CHECK (state NOT IN ('ready', 'deploying', 'active', 'superseded') OR artifact_digest IS NOT NULL),
    CHECK (state NOT IN ('ready', 'deploying', 'active', 'superseded') OR resolved_commit IS NOT NULL)
);

CREATE INDEX releases_service_page_idx
    ON porter_control_plane.releases (service_id, sequence DESC, id);

CREATE UNIQUE INDEX releases_one_active_per_service_idx
    ON porter_control_plane.releases (service_id)
    WHERE state = 'active';

CREATE FUNCTION porter_control_plane.guard_release_intent()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(NEW.id, NEW.service_id, NEW.sequence, NEW.source_kind, NEW.source_repository, NEW.source_ref, NEW.build_spec)
       IS DISTINCT FROM
       ROW(OLD.id, OLD.service_id, OLD.sequence, OLD.source_kind, OLD.source_repository, OLD.source_ref, OLD.build_spec) THEN
        RAISE EXCEPTION 'release source/build identity is immutable'
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

CREATE TRIGGER releases_immutable_intent
    BEFORE UPDATE ON porter_control_plane.releases
    FOR EACH ROW EXECUTE FUNCTION porter_control_plane.guard_release_intent();

CREATE TABLE porter_control_plane.instances (
    id           TEXT PRIMARY KEY CHECK (id <> '' AND octet_length(id) <= 128),
    service_id   TEXT NOT NULL,
    release_id   TEXT NOT NULL,
    ordinal      INTEGER NOT NULL CHECK (ordinal > 0),
    runtime      TEXT NOT NULL DEFAULT 'firecracker'
                     CHECK (runtime = 'firecracker'),
    state        TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'provisioning', 'running', 'stopped', 'failed', 'deleting', 'deleted')),
    health       TEXT NOT NULL DEFAULT 'unknown'
                     CHECK (health IN ('unknown', 'healthy', 'unhealthy')),
    node_id      TEXT REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (release_id, service_id)
        REFERENCES porter_control_plane.releases (id, service_id) ON DELETE RESTRICT,
    UNIQUE (release_id, ordinal)
);

CREATE INDEX instances_release_idx
    ON porter_control_plane.instances (release_id, ordinal, id);

CREATE INDEX instances_service_state_idx
    ON porter_control_plane.instances (service_id, state, id);

CREATE INDEX instances_node_state_idx
    ON porter_control_plane.instances (node_id, state, id)
    WHERE node_id IS NOT NULL;

COMMENT ON SCHEMA porter_control_plane IS
    'Versioned PostgreSQL control-plane foundation; separate from legacy schemas.';
COMMENT ON TABLE porter_control_plane.services IS
    'Deployable service configuration. Production workload runtime is Firecracker/KVM only.';
COMMENT ON TABLE porter_control_plane.releases IS
    'Immutable source/build intent and digest-addressed artifact lifecycle for a service.';
COMMENT ON TABLE porter_control_plane.instances IS
    'Firecracker guest instances associated with a release and optional fleet placement.';
