-- Porter PostgreSQL foundation, version 4. Storage and recovery metadata only.
-- This migration does not provision disks, capture snapshots, restore data, or write off-host backups.

ALTER TABLE porter_control_plane.instances
    ADD CONSTRAINT instances_id_service_id_key UNIQUE (id, service_id);

CREATE INDEX instances_service_page_idx
    ON porter_control_plane.instances (service_id, created_at, id);

CREATE TABLE porter_control_plane.volume_metadata (
    id                 TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    project_id         TEXT NOT NULL,
    service_id         TEXT NOT NULL,
    name               TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    size_mib           BIGINT NOT NULL CHECK (size_mib BETWEEN 1 AND 16777216),
    mount_path         TEXT NOT NULL DEFAULT ''
                           CHECK (length(mount_path) <= 4096)
                           CHECK (mount_path = '' OR mount_path ~ '^/([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*)?$')
                           CHECK (mount_path !~ '(^|/)([.]{1,2})(/|$)'),
    access_mode        TEXT NOT NULL DEFAULT 'single_writer'
                           CHECK (access_mode = 'single_writer'),
    state              TEXT NOT NULL DEFAULT 'metadata_only'
                           CHECK (state = 'metadata_only'),
    node_affinity_id   TEXT REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    integrity_status   TEXT NOT NULL DEFAULT 'not_available'
                           CHECK (integrity_status = 'not_available'),
    integrity_sha256   TEXT CHECK (integrity_sha256 IS NULL OR integrity_sha256 ~ '^[0-9a-f]{64}$'),
    integrity_checked_at TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, name),
    UNIQUE (id, project_id, service_id),
    FOREIGN KEY (service_id, project_id)
        REFERENCES porter_control_plane.services (id, project_id) ON DELETE RESTRICT,
    CHECK (integrity_sha256 IS NULL AND integrity_checked_at IS NULL)
);

CREATE INDEX volume_metadata_project_page_idx
    ON porter_control_plane.volume_metadata (project_id, created_at, id);

CREATE INDEX volume_metadata_service_page_idx
    ON porter_control_plane.volume_metadata (service_id, created_at, id);

CREATE INDEX volume_metadata_node_affinity_idx
    ON porter_control_plane.volume_metadata (node_affinity_id, id)
    WHERE node_affinity_id IS NOT NULL;

CREATE TABLE porter_control_plane.snapshot_metadata (
    id                   TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    project_id           TEXT NOT NULL,
    service_id           TEXT NOT NULL,
    instance_id          TEXT NOT NULL,
    consistency          TEXT NOT NULL DEFAULT 'crash_consistent'
                             CHECK (consistency = 'crash_consistent'),
    capture_status       TEXT NOT NULL DEFAULT 'unavailable'
                             CHECK (capture_status = 'unavailable'),
    node_affinity_id     TEXT REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    artifact_sha256      TEXT CHECK (artifact_sha256 IS NULL OR artifact_sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes           BIGINT CHECK (size_bytes IS NULL OR size_bytes >= 0),
    integrity_status     TEXT NOT NULL DEFAULT 'not_available'
                             CHECK (integrity_status = 'not_available'),
    integrity_checked_at TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, project_id, service_id, instance_id),
    FOREIGN KEY (service_id, project_id)
        REFERENCES porter_control_plane.services (id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (instance_id, service_id)
        REFERENCES porter_control_plane.instances (id, service_id) ON DELETE RESTRICT,
    CHECK (artifact_sha256 IS NULL AND size_bytes IS NULL AND integrity_checked_at IS NULL)
);

CREATE INDEX snapshot_metadata_project_page_idx
    ON porter_control_plane.snapshot_metadata (project_id, created_at, id);

CREATE INDEX snapshot_metadata_instance_page_idx
    ON porter_control_plane.snapshot_metadata (instance_id, created_at, id);

CREATE INDEX snapshot_metadata_node_affinity_idx
    ON porter_control_plane.snapshot_metadata (node_affinity_id, id)
    WHERE node_affinity_id IS NOT NULL;

CREATE TABLE porter_control_plane.backup_metadata (
    id                   TEXT PRIMARY KEY CHECK (length(btrim(id)) BETWEEN 1 AND 128),
    project_id           TEXT NOT NULL,
    service_id           TEXT NOT NULL,
    instance_id          TEXT NOT NULL,
    snapshot_id          TEXT NOT NULL,
    consistency          TEXT NOT NULL DEFAULT 'crash_consistent'
                             CHECK (consistency = 'crash_consistent'),
    state                TEXT NOT NULL DEFAULT 'metadata_only'
                             CHECK (state = 'metadata_only'),
    off_host_status      TEXT NOT NULL DEFAULT 'unavailable'
                             CHECK (off_host_status = 'unavailable'),
    node_affinity_id     TEXT REFERENCES porter_control_plane.fleet_nodes(id) ON DELETE RESTRICT,
    artifact_sha256      TEXT CHECK (artifact_sha256 IS NULL OR artifact_sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes           BIGINT CHECK (size_bytes IS NULL OR size_bytes >= 0),
    integrity_status     TEXT NOT NULL DEFAULT 'not_available'
                             CHECK (integrity_status = 'not_available'),
    integrity_checked_at TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, project_id, service_id, instance_id),
    FOREIGN KEY (service_id, project_id)
        REFERENCES porter_control_plane.services (id, project_id) ON DELETE RESTRICT,
    FOREIGN KEY (snapshot_id, project_id, service_id, instance_id)
        REFERENCES porter_control_plane.snapshot_metadata (id, project_id, service_id, instance_id) ON DELETE RESTRICT,
    CHECK (artifact_sha256 IS NULL AND size_bytes IS NULL AND integrity_checked_at IS NULL)
);

CREATE INDEX backup_metadata_project_page_idx
    ON porter_control_plane.backup_metadata (project_id, created_at, id);

CREATE INDEX backup_metadata_snapshot_page_idx
    ON porter_control_plane.backup_metadata (snapshot_id, created_at, id);

CREATE INDEX backup_metadata_node_affinity_idx
    ON porter_control_plane.backup_metadata (node_affinity_id, id)
    WHERE node_affinity_id IS NOT NULL;

ALTER TABLE porter_control_plane.resource_events
    ADD COLUMN volume_id TEXT,
    ADD COLUMN snapshot_id TEXT,
    ADD COLUMN backup_id TEXT;

CREATE INDEX resource_events_volume_page_idx
    ON porter_control_plane.resource_events (volume_id, occurred_at DESC, id DESC)
    WHERE volume_id IS NOT NULL;

CREATE INDEX resource_events_snapshot_page_idx
    ON porter_control_plane.resource_events (snapshot_id, occurred_at DESC, id DESC)
    WHERE snapshot_id IS NOT NULL;

CREATE INDEX resource_events_backup_page_idx
    ON porter_control_plane.resource_events (backup_id, occurred_at DESC, id DESC)
    WHERE backup_id IS NOT NULL;

COMMENT ON TABLE porter_control_plane.volume_metadata IS
    'Single-writer volume declarations only; no disk is provisioned, mounted, or attached.';
COMMENT ON TABLE porter_control_plane.snapshot_metadata IS
    'Crash-consistent snapshot metadata only; capture is unavailable and no artifact is present.';
COMMENT ON TABLE porter_control_plane.backup_metadata IS
    'Crash-consistent backup metadata only; off-host storage and recovery are unavailable.';
