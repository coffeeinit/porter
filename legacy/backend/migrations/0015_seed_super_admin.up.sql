-- Database-backed super-admin seed. Authorization still resolves through the
-- users, org_members, roles, permissions, and role_permissions tables.
INSERT INTO roles (id, name, description)
VALUES ('super_admin', 'Super Admin', 'Full control over all persisted Porter resources and host operations')
ON CONFLICT (id) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT 'super_admin', id FROM permissions
ON CONFLICT DO NOTHING;

UPDATE users
SET role = 'super_admin'
WHERE username = 'admin' AND role IN ('admin', 'owner', 'super_admin');

INSERT INTO org_members (org_id, user_id, role)
SELECT o.id, u.id, 'owner'
FROM orgs o
JOIN users u ON u.username = 'admin'
WHERE o.owner_id = u.username AND o.is_default = true
ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role;
