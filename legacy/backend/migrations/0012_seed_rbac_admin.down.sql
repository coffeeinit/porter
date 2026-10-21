-- Remove only an untouched blank-password seed row; never delete an initialized
-- or actively used account during rollback.
DELETE FROM users WHERE username = 'admin' AND password_hash = '';
