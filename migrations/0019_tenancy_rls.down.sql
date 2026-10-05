-- 0019_tenancy_rls rollback: drop policies + helper, keep RLS enabled state
-- removal minimal (policies gone = open access as before).
DROP POLICY IF EXISTS tenant_isolation ON projects;
DROP POLICY IF EXISTS tenant_isolation ON environments;
DROP POLICY IF EXISTS tenant_isolation ON secrets;
DROP POLICY IF EXISTS tenant_isolation ON volumes;
DROP POLICY IF EXISTS tenant_isolation ON networks;
DROP POLICY IF EXISTS tenant_isolation ON domains;
DROP FUNCTION IF EXISTS porter_tenant();
ALTER TABLE projects NO FORCE ROW LEVEL SECURITY;
ALTER TABLE environments NO FORCE ROW LEVEL SECURITY;
ALTER TABLE secrets NO FORCE ROW LEVEL SECURITY;
ALTER TABLE volumes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE networks NO FORCE ROW LEVEL SECURITY;
ALTER TABLE domains NO FORCE ROW LEVEL SECURITY;
