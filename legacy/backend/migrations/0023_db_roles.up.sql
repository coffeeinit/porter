-- 0023_db_roles: roles live in the database, not in code.
-- Adds policy flags to roles so Go never hardcodes role names:
--   scope     platform|org|project|shared (informational grouping)
--   is_admin  bypasses tenancy gates (platform super admin)
--   sees_all  lists every project without membership (platform operators)
--   is_default  assigned when callers omit a role ( Railway-style onboarding )
--   is_system  protected seed roles (cannot be deleted via the API)
ALTER TABLE roles ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'shared';
ALTER TABLE roles ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE roles ADD COLUMN IF NOT EXISTS sees_all BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE roles ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE roles ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT false;

UPDATE roles SET scope='platform', is_admin=true, sees_all=true, is_system=true
WHERE id='super_admin';
UPDATE roles SET scope='shared', sees_all=true, is_system=true
WHERE id IN ('owner','admin');
UPDATE roles SET scope='shared', is_default=true, is_system=true
WHERE id='member';
UPDATE roles SET scope='shared', is_system=true
WHERE id='viewer';
