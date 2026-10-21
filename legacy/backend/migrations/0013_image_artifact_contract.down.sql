ALTER TABLE golden_images DROP COLUMN IF EXISTS validated_at;
ALTER TABLE golden_images DROP COLUMN IF EXISTS status;
ALTER TABLE golden_images DROP COLUMN IF EXISTS kernel_sha256;
ALTER TABLE golden_images DROP COLUMN IF EXISTS rootfs_sha256;
ALTER TABLE golden_images DROP COLUMN IF EXISTS kernel_path;
ALTER TABLE golden_images DROP COLUMN IF EXISTS rootfs_path;
ALTER TABLE golden_images DROP COLUMN IF EXISTS architecture;
ALTER TABLE golden_images DROP COLUMN IF EXISTS kind;
