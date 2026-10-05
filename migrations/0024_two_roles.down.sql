-- Down restores the pre-collapse role rows minimally (grants for retired roles
-- are NOT reconstructed; re-run 0007 seeds to restore them fully).
ALTER TABLE projects DROP COLUMN IF EXISTS created_by;
INSERT INTO roles (id, name, description, scope, is_admin, sees_all, is_default, is_system) VALUES
    ('owner', 'Owner', 'Full control, including member management and project deletion',
     'shared', false, true, false, true),
    ('viewer', 'Viewer', 'Read-only access to a project',
     'shared', false, false, false, true),
    ('super_admin', 'Super Admin', 'Platform super administrator',
     'platform', true, true, false, true)
ON CONFLICT DO NOTHING;
