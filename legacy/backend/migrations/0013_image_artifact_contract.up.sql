-- Explicit artifact metadata for direct Firecracker image readiness. The
-- existing data JSON remains for compatibility, while these columns make
-- readiness and provenance queryable by operators and future schedulers.
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS architecture TEXT NOT NULL DEFAULT 'x86_64';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS rootfs_path TEXT NOT NULL DEFAULT '';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS kernel_path TEXT NOT NULL DEFAULT '';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS rootfs_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS kernel_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE golden_images ADD COLUMN IF NOT EXISTS validated_at TIMESTAMPTZ;
