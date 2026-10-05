-- 0017_scoped_rbac_tenancy_compute rollback.
DROP TABLE IF EXISTS micro_vms;
DROP TABLE IF EXISTS ip_allocations;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS resellers;
DROP TABLE IF EXISTS role_assignments;
ALTER TABLE role_permissions DROP COLUMN IF EXISTS effect;
