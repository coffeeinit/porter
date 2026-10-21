-- =============================================================
-- 0019_tenancy_rls: defense-in-depth row security on tenancy tables (T4b).
-- STATUS: policies are permissive-when-unset by design -- the API middleware
-- is the enforcer today; these policies re-enforce at the query layer once
-- callers SET LOCAL app.tenant_id (tx-aware paths, future work).
-- Behavior change today: NONE (current_setting(..., true) IS NULL normally).
-- FORCE is set so policies apply even to table owners; the empty-tenant
-- branch keeps every existing query working until scopes are threaded.
-- =============================================================

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE ROW LEVEL SECURITY;
ALTER TABLE environments ENABLE ROW LEVEL SECURITY;
ALTER TABLE environments FORCE ROW LEVEL SECURITY;
ALTER TABLE secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE secrets FORCE ROW LEVEL SECURITY;
ALTER TABLE volumes ENABLE ROW LEVEL SECURITY;
ALTER TABLE volumes FORCE ROW LEVEL SECURITY;
ALTER TABLE networks ENABLE ROW LEVEL SECURITY;
ALTER TABLE networks FORCE ROW LEVEL SECURITY;
ALTER TABLE domains ENABLE ROW LEVEL SECURITY;
ALTER TABLE domains FORCE ROW LEVEL SECURITY;

-- Helper: current tenant for this session, '' when unset.
CREATE OR REPLACE FUNCTION porter_tenant() RETURNS text AS $$
  SELECT COALESCE(current_setting('app.tenant_id', true), '')
$$ LANGUAGE sql STABLE;

DROP POLICY IF EXISTS tenant_isolation ON projects;
CREATE POLICY tenant_isolation ON projects FOR ALL USING (
  porter_tenant() = '' OR org_id::text = porter_tenant() OR id::text = porter_tenant()
);

DROP POLICY IF EXISTS tenant_isolation ON environments;
CREATE POLICY tenant_isolation ON environments FOR ALL USING (
  porter_tenant() = ''
  OR project_id::text IN (SELECT id::text FROM projects WHERE org_id::text = porter_tenant())
  OR project_id::text = porter_tenant()
);

DROP POLICY IF EXISTS tenant_isolation ON secrets;
CREATE POLICY tenant_isolation ON secrets FOR ALL USING (
  porter_tenant() = '' OR project_id::text = porter_tenant()
);

DROP POLICY IF EXISTS tenant_isolation ON volumes;
-- NOTE: volumes has no project/org column yet; this policy stays permissive
-- until a project linkage lands (future migration). See docs/plan-additions.md.
CREATE POLICY tenant_isolation ON volumes FOR ALL USING (
  porter_tenant() = ''
);

DROP POLICY IF EXISTS tenant_isolation ON networks;
CREATE POLICY tenant_isolation ON networks FOR ALL USING (
  porter_tenant() = '' OR project_id::text = porter_tenant()
);

DROP POLICY IF EXISTS tenant_isolation ON domains;
CREATE POLICY tenant_isolation ON domains FOR ALL USING (
  porter_tenant() = '' OR project_id::text = porter_tenant()
);
