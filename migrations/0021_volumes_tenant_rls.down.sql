DROP POLICY IF EXISTS tenant_isolation ON volumes;
CREATE POLICY tenant_isolation ON volumes FOR ALL USING (porter_tenant() = '');
