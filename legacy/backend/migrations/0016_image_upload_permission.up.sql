-- Direct Firecracker image upload is an authenticated write operation. Keep it
-- in the database-seeded RBAC model rather than relying on auth-only access.
INSERT INTO permissions (id, name, description)
VALUES ('image.upload', 'Upload direct Firecracker image', 'Register a user-supplied vmlinux + rootfs.ext4 bundle')
ON CONFLICT (id) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT id, 'image.upload'
FROM roles
WHERE id IN ('member', 'admin', 'owner', 'super_admin')
ON CONFLICT (role_id, permission_id) DO NOTHING;
