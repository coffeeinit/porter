-- 0030_mcp_cf_email_ops rollback (seeds only; user grants on these codes
-- are removed with them).
DELETE FROM role_permissions WHERE permission_id IN
    ('mcp.search', 'mcp.describe', 'mcp.call',
     'email.send', 'email.manage',
     'cf.tunnel', 'cf.dns',
     'kernel.build', 'vm.migrate',
     'share.create', 'share.delete',
     'billing.read', 'billing.manage',
     'template.read', 'template.manage');
DELETE FROM permissions WHERE id IN
    ('mcp.search', 'mcp.describe', 'mcp.call',
     'email.send', 'email.manage',
     'cf.tunnel', 'cf.dns',
     'kernel.build', 'vm.migrate',
     'share.create', 'share.delete',
     'billing.read', 'billing.manage',
     'template.read', 'template.manage');
