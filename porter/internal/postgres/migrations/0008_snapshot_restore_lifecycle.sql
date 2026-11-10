-- Porter PostgreSQL foundation, version 8. Typed crash-consistent snapshot lifecycle.
-- Artifacts remain on the assigned agent host; this schema stores identity and integrity metadata only.

ALTER TABLE porter_control_plane.instances
    ADD COLUMN runtime_vm_id TEXT;

CREATE UNIQUE INDEX instances_node_runtime_vm_id_idx
    ON porter_control_plane.instances (node_id, runtime_vm_id)
    WHERE node_id IS NOT NULL AND runtime_vm_id IS NOT NULL;

ALTER TABLE porter_control_plane.snapshot_metadata
    ADD COLUMN capture_operation_id TEXT,
    ADD COLUMN runtime_vm_id TEXT,
    ADD COLUMN restore_status TEXT NOT NULL DEFAULT 'not_requested',
    ADD COLUMN restore_operation_id TEXT;

ALTER TABLE porter_control_plane.snapshot_metadata
    DROP CONSTRAINT snapshot_metadata_capture_status_check,
    DROP CONSTRAINT snapshot_metadata_integrity_status_check,
    DROP CONSTRAINT snapshot_metadata_check;

ALTER TABLE porter_control_plane.snapshot_metadata
    ADD CONSTRAINT snapshot_metadata_capture_status_check
        CHECK (capture_status IN ('unavailable', 'queued', 'capturing', 'captured', 'failed')),
    ADD CONSTRAINT snapshot_metadata_integrity_status_check
        CHECK (integrity_status IN ('not_available', 'verified', 'failed')),
    ADD CONSTRAINT snapshot_metadata_capture_operation_fk
        FOREIGN KEY (capture_operation_id, project_id, service_id, instance_id)
        REFERENCES porter_control_plane.operations (id, project_id, service_id, instance_id) ON DELETE RESTRICT,
    ADD CONSTRAINT snapshot_metadata_restore_operation_fk
        FOREIGN KEY (restore_operation_id, project_id, service_id, instance_id)
        REFERENCES porter_control_plane.operations (id, project_id, service_id, instance_id) ON DELETE RESTRICT,
    ADD CONSTRAINT snapshot_metadata_restore_status_check
        CHECK (restore_status IN ('not_requested', 'queued', 'running', 'restored', 'failed')),
    ADD CONSTRAINT snapshot_metadata_capture_operation_check
        CHECK ((capture_status = 'unavailable' AND capture_operation_id IS NULL)
            OR (capture_status <> 'unavailable' AND capture_operation_id IS NOT NULL)),
    ADD CONSTRAINT snapshot_metadata_restore_operation_check
        CHECK ((restore_status = 'not_requested' AND restore_operation_id IS NULL)
            OR (restore_status <> 'not_requested' AND restore_operation_id IS NOT NULL)),
    ADD CONSTRAINT snapshot_metadata_artifact_lifecycle_check
        CHECK (
            (capture_status = 'captured' AND artifact_sha256 IS NOT NULL AND size_bytes > 0
                AND integrity_status = 'verified' AND integrity_checked_at IS NOT NULL)
            OR (capture_status <> 'captured' AND artifact_sha256 IS NULL AND size_bytes IS NULL
                AND integrity_status = 'not_available' AND integrity_checked_at IS NULL)
        );

CREATE INDEX snapshot_metadata_capture_operation_idx
    ON porter_control_plane.snapshot_metadata (capture_operation_id)
    WHERE capture_operation_id IS NOT NULL;
CREATE INDEX snapshot_metadata_restore_operation_idx
    ON porter_control_plane.snapshot_metadata (restore_operation_id)
    WHERE restore_operation_id IS NOT NULL;

ALTER TABLE porter_control_plane.agent_command_results
    ADD COLUMN snapshot_artifact JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(snapshot_artifact) = 'object');

COMMENT ON COLUMN porter_control_plane.instances.runtime_vm_id IS
    'Agent-local runtime identity learned from a fenced successful v2 VM command; never includes a socket path.';
COMMENT ON COLUMN porter_control_plane.snapshot_metadata.artifact_sha256 IS
    'Aggregate digest over crash-consistent state, memory, and disk checksums; files and paths remain agent-local.';
COMMENT ON COLUMN porter_control_plane.agent_command_results.snapshot_artifact IS
    'Compact path-free snapshot integrity receipt; never stores Firecracker socket or local artifact paths.';
