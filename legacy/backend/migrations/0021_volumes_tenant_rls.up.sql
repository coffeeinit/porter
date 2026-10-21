-- 0021_volumes_tenant_rls: tighten the permissive 0019 volumes policy now that
-- volumes.project_id exists in the store (PutVolume/ListVolumes). Same shape as
-- secrets/networks/domains: empty tenant = no behavior change; set tenant =
-- project-scoped isolation at the query layer (defense in depth over API RBAC).
DROP POLICY IF EXISTS tenant_isolation ON volumes;
CREATE POLICY tenant_isolation ON volumes FOR ALL USING (
  porter_tenant() = '' OR project_id::text = porter_tenant()
);
