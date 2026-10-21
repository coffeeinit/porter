-- 0018_event_spine_task_ledger_idempotency rollback.
DELETE FROM role_permissions WHERE permission_id IN (
    'replica.snapshot', 'replica.restore',
    'network.create', 'network.read', 'network.delete',
    'certificate.read', 'certificate.issue',
    'event.read', 'audit.read'
);
DELETE FROM permissions WHERE id IN (
    'replica.snapshot', 'replica.restore',
    'network.create', 'network.read', 'network.delete',
    'certificate.read', 'certificate.issue',
    'event.read', 'audit.read'
);
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS events;
