DELETE FROM org_members
WHERE user_id IN (SELECT id FROM users WHERE username = 'admin')
  AND role = 'owner';
UPDATE users SET role = 'admin' WHERE username = 'admin' AND role = 'super_admin';
DELETE FROM role_permissions WHERE role_id = 'super_admin';
DELETE FROM roles WHERE id = 'super_admin';
