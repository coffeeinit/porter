-- v1.0.0-beta-dev: seed the initial database principal and leave its
-- password blank until PORTER_BOOTSTRAP_ADMIN_PASSWORD initializes it once.
-- No plaintext credential or API token is stored in migrations.
INSERT INTO users (username, role, password_hash, salt, data)
VALUES ('admin', 'admin', '', '', '{}'::jsonb)
ON CONFLICT (username) DO NOTHING;

-- The admin's default org is also persisted; all later membership and
-- permission checks resolve through these database rows.
INSERT INTO orgs (owner_id, name, is_default)
SELECT u.username, 'Default organization', true
FROM users u
WHERE u.username = 'admin'
  AND NOT EXISTS (
    SELECT 1 FROM orgs o WHERE o.owner_id = u.username AND o.is_default = true
  );
