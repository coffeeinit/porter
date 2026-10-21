-- 0025_member_share: members fully manage projects they belong to (delete,
-- transfer, invite/remove collaborators). Tenancy gates still restrict them
-- to member projects/teams/orgs; these verbs only work inside membership.
INSERT INTO role_permissions (role_id, permission_id) VALUES
    ('member', 'project.delete'),
    ('member', 'project.transfer'),
    ('member', 'member.invite'),
    ('member', 'member.remove')
ON CONFLICT DO NOTHING;
