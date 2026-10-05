DELETE FROM role_permissions WHERE role_id='member' AND permission_id IN
    ('project.delete', 'project.transfer', 'member.invite', 'member.remove');
