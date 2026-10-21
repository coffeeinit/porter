-- =============================================================
-- RBAC: roles, permissions, and role→permission mapping.
-- Permissions are fine-grained "<resource>.<action>" codes named after the
-- route they guard (e.g. ssh.create, ssh.delete, ssh.connect, project.delete,
-- deployment.promote, member.invite). Access checks resolve a user's roles
-- (global, org, project) to these concrete permission codes.
-- =============================================================

CREATE TABLE IF NOT EXISTS roles (
    id          TEXT PRIMARY KEY,             -- owner | admin | member | viewer
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS permissions (
    id          TEXT PRIMARY KEY,             -- e.g. project.read, ssh.connect
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id TEXT NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

-- Seed roles (extensible: more roles are just new rows).
INSERT INTO roles (id, name, description) VALUES
    ('owner',  'Owner',  'Full control, including member management and project deletion'),
    ('admin',  'Admin',  'Administrative control across the platform'),
    ('member', 'Member', 'Can build, deploy, scale, and configure a project'),
    ('viewer', 'Viewer', 'Read-only access to a project')
ON CONFLICT (id) DO NOTHING;

-- Seed fine-grained permissions. Every row names one specific action on one
-- resource so the UI can render and edit them as individual capabilities.
INSERT INTO permissions (id, name) VALUES
    -- projects
    ('project.list',         'List projects'),
    ('project.read',         'Read project'),
    ('project.create',       'Create project'),
    ('project.rename',       'Rename project'),
    ('project.delete',       'Delete project'),
    ('project.transfer',     'Transfer project'),
    ('project.export',       'Export project'),
    ('project.import',       'Import project'),
    ('project.avatar',       'Change project avatar'),
    ('project.deploy',       'Deploy / redeploy project'),
    ('project.scale',        'Change replica count'),
    ('project.restart',      'Restart project'),
    ('project.network',      'Manage project networks'),
    ('project.settings',     'Change project settings'),
    ('user.list',            'List users'),
    ('user.read',            'Read a user'),
    -- env & secrets
    ('env.list',             'List environment variables'),
    ('env.set',              'Set environment variables'),
    ('secret.list',          'List secrets'),
    ('secret.create',        'Create secret'),
    ('secret.delete',        'Delete secret'),
    -- domains & dns
    ('domain.list',          'List domains'),
    ('domain.add',           'Add domain'),
    ('domain.remove',        'Remove domain'),
    ('domain.verify',        'Verify domain'),
    -- deployments / builds / git
    ('deployment.list',      'List deployments'),
    ('deployment.create',    'Create deployment'),
    ('deployment.promote',   'Promote deployment to production'),
    ('deployment.rollback',  'Rollback deployment'),
    ('build.create',         'Start a build'),
    ('git.import',           'Import a git repository'),
    ('git.settings',         'Change git settings'),
    -- replicas / ssh / exec / console
    ('replica.list',         'List replicas'),
    ('replica.start',        'Start a replica'),
    ('replica.stop',         'Stop a replica'),
    ('replica.restart',      'Restart a replica'),
    ('replica.delete',       'Delete a replica'),
    ('replica.exec',         'Execute a command in a replica'),
    ('ssh.connect',          'Open an SSH session'),
    ('ssh.toggle',           'Toggle SSH for a project'),
    ('console.open',         'Open an interactive console'),
    -- observability
    ('log.read',             'Read logs'),
    ('traffic.read',         'Read traffic'),
    ('metric.read',          'Read metrics'),
    ('analytics.read',       'Read analytics'),
    ('webvital.read',        'Read web vitals'),
    ('event.read',           'Read events'),
    -- crons / hooks / drains / alerts / redirects / firewall / cache / volumes
    ('cron.create',          'Create cron job'),
    ('cron.update',          'Edit cron job'),
    ('cron.delete',          'Delete cron job'),
    ('cron.run',             'Run cron job'),
    ('hook.create',          'Create webhook'),
    ('hook.delete',          'Delete webhook'),
    ('hook.trigger',         'Trigger webhook'),
    ('drain.create',         'Create log drain'),
    ('drain.delete',         'Delete log drain'),
    ('alert.create',         'Create alert'),
    ('alert.update',         'Edit alert'),
    ('alert.delete',         'Delete alert'),
    ('alert.silence',        'Silence alert'),
    ('redirect.create',      'Create redirect'),
    ('redirect.delete',      'Delete redirect'),
    ('firewall.create',      'Create firewall rule'),
    ('firewall.delete',      'Delete firewall rule'),
    ('firewall.update',      'Edit firewall rule'),
    ('cache.purge',          'Purge cache'),
    ('cache.stats',          'Read cache stats'),
    ('volume.create',        'Create volume'),
    ('volume.resize',        'Resize volume'),
    ('volume.delete',        'Delete volume'),
    ('volume.read',          'Read volume'),
    -- members / org / users / servers / auth
    ('member.list',          'List members'),
    ('member.invite',        'Invite member'),
    ('member.remove',        'Remove member'),
    ('member.role',          'Change member role'),
    ('org.settings',         'Change org settings'),
    ('org.transfer',         'Transfer org'),
    ('org.audit',            'Read org audit log'),
    ('org.member.add',       'Add org member'),
    ('org.member.remove',    'Remove org member'),
    ('org.member.role',      'Change org member role'),
    ('group.create',         'Create group'),
    ('group.update',         'Edit group'),
    ('group.delete',         'Delete group'),
    ('user.create',          'Create user'),
    ('user.delete',          'Delete user'),
    ('server.register',      'Register a server'),
    ('server.remove',        'Remove a server'),
    ('apikey.create',        'Create API key'),
    ('apikey.delete',        'Delete API key'),
    ('feedback.write',       'Submit feedback'),
    ('feedback.read',        'Read feedback')
ON CONFLICT (id) DO NOTHING;

-- Map roles → permissions. The source of truth for what each role may do;
-- edit rows here (or in the UI) instead of changing code to adjust policy.
INSERT INTO role_permissions (role_id, permission_id)
SELECT v.role_id, v.permission_id
FROM (VALUES
    -- viewer: read-only
    ('viewer', 'project.list'), ('viewer', 'project.read'),
    ('viewer', 'env.list'), ('viewer', 'secret.list'),
    ('viewer', 'domain.list'), ('viewer', 'deployment.list'),
    ('viewer', 'replica.list'), ('viewer', 'log.read'),
    ('viewer', 'traffic.read'), ('viewer', 'metric.read'),
    ('viewer', 'analytics.read'), ('viewer', 'webvital.read'),
    ('viewer', 'event.read'), ('viewer', 'cache.stats'),
    ('viewer', 'volume.read'), ('viewer', 'member.list'),
    ('viewer', 'org.audit'),
    -- member: everything a viewer can do plus writes on project resources
    ('member', 'project.list'), ('member', 'project.read'),
    ('member', 'project.create'), ('member', 'project.rename'),
    ('member', 'project.deploy'), ('member', 'project.scale'),
    ('member', 'project.restart'), ('member', 'project.network'),
    ('member', 'project.settings'), ('member', 'project.export'),
    ('member', 'project.import'), ('member', 'project.avatar'),
    ('member', 'env.list'), ('member', 'env.set'),
    ('member', 'secret.list'), ('member', 'secret.create'), ('member', 'secret.delete'),
    ('member', 'domain.list'), ('member', 'domain.add'),
    ('member', 'domain.remove'), ('member', 'domain.verify'),
    ('member', 'deployment.list'), ('member', 'deployment.create'),
    ('member', 'deployment.promote'), ('member', 'deployment.rollback'),
    ('member', 'build.create'), ('member', 'git.import'), ('member', 'git.settings'),
    ('member', 'replica.list'), ('member', 'replica.start'),
    ('member', 'replica.stop'), ('member', 'replica.restart'),
    ('member', 'replica.delete'), ('member', 'replica.exec'),
    ('member', 'ssh.connect'), ('member', 'ssh.toggle'), ('member', 'console.open'),
    ('member', 'log.read'), ('member', 'traffic.read'), ('member', 'metric.read'),
    ('member', 'analytics.read'), ('member', 'webvital.read'), ('member', 'event.read'),
    ('member', 'cron.create'), ('member', 'cron.update'), ('member', 'cron.delete'), ('member', 'cron.run'),
    ('member', 'hook.create'), ('member', 'hook.delete'), ('member', 'hook.trigger'),
    ('member', 'drain.create'), ('member', 'drain.delete'),
    ('member', 'alert.create'), ('member', 'alert.update'), ('member', 'alert.delete'), ('member', 'alert.silence'),
    ('member', 'redirect.create'), ('member', 'redirect.delete'),
    ('member', 'firewall.create'), ('member', 'firewall.delete'), ('member', 'firewall.update'),
    ('member', 'cache.purge'), ('member', 'cache.stats'),
    ('member', 'volume.create'), ('member', 'volume.resize'), ('member', 'volume.delete'), ('member', 'volume.read'),
    ('member', 'member.list'),
    ('member', 'feedback.write'),
    -- admin: member plus org/user/host management and role changes
    ('admin', 'project.list'), ('admin', 'project.read'),
    ('admin', 'project.create'), ('admin', 'project.rename'),
    ('admin', 'project.deploy'), ('admin', 'project.scale'),
    ('admin', 'project.restart'), ('admin', 'project.network'),
    ('admin', 'project.settings'), ('admin', 'project.export'),
    ('admin', 'project.import'), ('admin', 'project.avatar'),
    ('admin', 'env.list'), ('admin', 'env.set'),
    ('admin', 'secret.list'), ('admin', 'secret.create'), ('admin', 'secret.delete'),
    ('admin', 'domain.list'), ('admin', 'domain.add'),
    ('admin', 'domain.remove'), ('admin', 'domain.verify'),
    ('admin', 'deployment.list'), ('admin', 'deployment.create'),
    ('admin', 'deployment.promote'), ('admin', 'deployment.rollback'),
    ('admin', 'build.create'), ('admin', 'git.import'), ('admin', 'git.settings'),
    ('admin', 'replica.list'), ('admin', 'replica.start'),
    ('admin', 'replica.stop'), ('admin', 'replica.restart'),
    ('admin', 'replica.delete'), ('admin', 'replica.exec'),
    ('admin', 'ssh.connect'), ('admin', 'ssh.toggle'), ('admin', 'console.open'),
    ('admin', 'log.read'), ('admin', 'traffic.read'), ('admin', 'metric.read'),
    ('admin', 'analytics.read'), ('admin', 'webvital.read'), ('admin', 'event.read'),
    ('admin', 'cron.create'), ('admin', 'cron.update'), ('admin', 'cron.delete'), ('admin', 'cron.run'),
    ('admin', 'hook.create'), ('admin', 'hook.delete'), ('admin', 'hook.trigger'),
    ('admin', 'drain.create'), ('admin', 'drain.delete'),
    ('admin', 'alert.create'), ('admin', 'alert.update'), ('admin', 'alert.delete'), ('admin', 'alert.silence'),
    ('admin', 'redirect.create'), ('admin', 'redirect.delete'),
    ('admin', 'firewall.create'), ('admin', 'firewall.delete'), ('admin', 'firewall.update'),
    ('admin', 'cache.purge'), ('admin', 'cache.stats'),
    ('admin', 'volume.create'), ('admin', 'volume.resize'), ('admin', 'volume.delete'), ('admin', 'volume.read'),
    ('admin', 'member.list'), ('admin', 'member.invite'),
    ('admin', 'member.remove'), ('admin', 'member.role'),
    ('admin', 'org.settings'), ('admin', 'org.transfer'), ('admin', 'org.audit'),
    ('admin', 'org.member.add'), ('admin', 'org.member.remove'), ('admin', 'org.member.role'),
    ('admin', 'group.create'), ('admin', 'group.update'), ('admin', 'group.delete'),
    ('admin', 'user.list'), ('admin', 'user.read'),
    ('admin', 'user.create'), ('admin', 'user.delete'),
    ('admin', 'server.register'), ('admin', 'server.remove'),
    ('admin', 'apikey.create'), ('admin', 'apikey.delete'),
    ('admin', 'feedback.write'), ('admin', 'feedback.read'),
    -- owner: everything including project deletion and transfer
    ('owner', 'project.list'), ('owner', 'project.read'),
    ('owner', 'project.create'), ('owner', 'project.rename'),
    ('owner', 'project.delete'), ('owner', 'project.transfer'),
    ('owner', 'project.deploy'), ('owner', 'project.scale'),
    ('owner', 'project.restart'), ('owner', 'project.network'),
    ('owner', 'project.settings'), ('owner', 'project.export'),
    ('owner', 'project.import'), ('owner', 'project.avatar'),
    ('owner', 'env.list'), ('owner', 'env.set'),
    ('owner', 'secret.list'), ('owner', 'secret.create'), ('owner', 'secret.delete'),
    ('owner', 'domain.list'), ('owner', 'domain.add'),
    ('owner', 'domain.remove'), ('owner', 'domain.verify'),
    ('owner', 'deployment.list'), ('owner', 'deployment.create'),
    ('owner', 'deployment.promote'), ('owner', 'deployment.rollback'),
    ('owner', 'build.create'), ('owner', 'git.import'), ('owner', 'git.settings'),
    ('owner', 'replica.list'), ('owner', 'replica.start'),
    ('owner', 'replica.stop'), ('owner', 'replica.restart'),
    ('owner', 'replica.delete'), ('owner', 'replica.exec'),
    ('owner', 'ssh.connect'), ('owner', 'ssh.toggle'), ('owner', 'console.open'),
    ('owner', 'log.read'), ('owner', 'traffic.read'), ('owner', 'metric.read'),
    ('owner', 'analytics.read'), ('owner', 'webvital.read'), ('owner', 'event.read'),
    ('owner', 'cron.create'), ('owner', 'cron.update'), ('owner', 'cron.delete'), ('owner', 'cron.run'),
    ('owner', 'hook.create'), ('owner', 'hook.delete'), ('owner', 'hook.trigger'),
    ('owner', 'drain.create'), ('owner', 'drain.delete'),
    ('owner', 'alert.create'), ('owner', 'alert.update'), ('owner', 'alert.delete'), ('owner', 'alert.silence'),
    ('owner', 'redirect.create'), ('owner', 'redirect.delete'),
    ('owner', 'firewall.create'), ('owner', 'firewall.delete'), ('owner', 'firewall.update'),
    ('owner', 'cache.purge'), ('owner', 'cache.stats'),
    ('owner', 'volume.create'), ('owner', 'volume.resize'), ('owner', 'volume.delete'), ('owner', 'volume.read'),
    ('owner', 'member.list'), ('owner', 'member.invite'),
    ('owner', 'member.remove'), ('owner', 'member.role'),
    ('owner', 'org.settings'), ('owner', 'org.transfer'), ('owner', 'org.audit'),
    ('owner', 'org.member.add'), ('owner', 'org.member.remove'), ('owner', 'org.member.role'),
    ('owner', 'group.create'), ('owner', 'group.update'), ('owner', 'group.delete'),
    ('owner', 'user.create'), ('owner', 'user.delete'),
    ('owner', 'server.register'), ('owner', 'server.remove'),
    ('owner', 'apikey.create'), ('owner', 'apikey.delete'),
    ('owner', 'feedback.write'), ('owner', 'feedback.read')
) AS v(role_id, permission_id)
WHERE NOT EXISTS (
    SELECT 1 FROM role_permissions rp WHERE rp.role_id = v.role_id AND rp.permission_id = v.permission_id
);
