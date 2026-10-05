-- 0024_two_roles: two-role model — exactly two roles.
--   admin  (platform super admin: everything, every org, every project)
--   member (everything inside teams/projects they belong to; nothing outside)
-- Ownership is a membership row + projects.created_by, never a third role.
-- Custom roles created via the API keep working (untouched).
INSERT INTO roles (id, name, description, scope, is_admin, sees_all, is_default, is_system) VALUES
    ('admin', 'Admin', 'Full platform control across every org, team and project',
     'platform', true, true, false, true),
    ('member', 'Member', 'Full control inside teams and projects they belong to',
     'shared', false, false, true, true)
ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, description=EXCLUDED.description,
    scope=EXCLUDED.scope, is_admin=EXCLUDED.is_admin, sees_all=EXCLUDED.sees_all,
    is_default=EXCLUDED.is_default, is_system=EXCLUDED.is_system;

-- admin inherits every historic grant (owner+admin+super_admin union).
INSERT INTO role_permissions (role_id, permission_id)
SELECT 'admin', permission_id FROM role_permissions
WHERE role_id IN ('owner', 'admin', 'super_admin')
ON CONFLICT DO NOTHING;

-- member keeps its seeded project/team grant set; ensure the sharing verbs.
INSERT INTO role_permissions (role_id, permission_id) VALUES
    ('member', 'project.create'), ('member', 'project.list'),
    ('member', 'member.list'), ('member', 'member.invite'),
    ('member', 'group.create')
ON CONFLICT DO NOTHING;

-- Collapse users onto the two roles (owner/admin/super_admin -> admin).
UPDATE users SET role='admin' WHERE role IN ('super_admin', 'owner', 'admin');
UPDATE users SET role='member' WHERE role NOT IN ('admin', 'member');

-- Collapse membership rows onto the two roles (anything else -> member).
UPDATE org_members SET role='member' WHERE role NOT IN ('admin', 'member');
UPDATE project_members SET role='member' WHERE role NOT IN ('admin', 'member');
UPDATE team_members SET role='member' WHERE role NOT IN ('admin', 'member');

-- Retire the old role rows (cascades their grants; users/members remapped).
DELETE FROM roles WHERE id NOT IN ('admin', 'member');

-- Project ownership without a role: who created it.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
