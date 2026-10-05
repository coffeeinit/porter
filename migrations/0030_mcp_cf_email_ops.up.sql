-- 0030_mcp_cf_email_ops: capability seeds for the MCP facade, Cloudflare
-- user-token tunnels, per-domain email, kernel builds, and VM migration.
-- Additive only; grants follow the two-role model (admin = platform,
-- member = inside their projects; project routes stay membership-gated).
INSERT INTO permissions (id, name) VALUES
    ('mcp.search',  'Search MCP tools'),
    ('mcp.describe', 'Describe MCP tool'),
    ('mcp.call',    'Call MCP tool'),
    ('email.send',  'Send email from project domain'),
    ('email.manage','Manage email identities'),
    ('cf.tunnel',   'Manage Cloudflare tunnels'),
    ('cf.dns',      'Manage Cloudflare DNS'),
    ('kernel.build','Build platform kernel'),
    ('vm.migrate',  'Migrate VM to another node'),
    ('share.create','Open quick-share tunnel'),
    ('share.delete','Close quick-share tunnel'),
    ('billing.read','Read billing and invoices'),
    ('billing.manage','Manage plans and subscriptions'),
    ('template.read','Read service templates'),
    ('template.manage','Manage service templates')
ON CONFLICT (id) DO NOTHING;

-- admin + owner: everything.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN (VALUES
    ('mcp.search'), ('mcp.describe'), ('mcp.call'),
    ('email.send'), ('email.manage'),
    ('cf.tunnel'), ('cf.dns'),
    ('kernel.build'), ('vm.migrate'),
    ('share.create'), ('share.delete'),
    ('billing.read'), ('billing.manage'),
    ('template.read'), ('template.manage')
) AS p(id)
WHERE r.id IN ('admin', 'owner')
ON CONFLICT DO NOTHING;

-- member: MCP use + email + tunnels/DNS/migrate inside their projects
-- (project routes remain membership-gated in granted()).
INSERT INTO role_permissions (role_id, permission_id) VALUES
    ('member', 'mcp.search'), ('member', 'mcp.describe'), ('member', 'mcp.call'),
    ('member', 'email.send'), ('member', 'cf.tunnel'), ('member', 'cf.dns'),
    ('member', 'vm.migrate'),
    ('member', 'share.create'), ('member', 'share.delete'),
    ('member', 'billing.read'), ('member', 'template.read')
ON CONFLICT DO NOTHING;
