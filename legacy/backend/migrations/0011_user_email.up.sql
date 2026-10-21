-- =============================================================
-- Per-user email for notifications: users gain an email column so the
-- SMTP mailer can resolve project members' real addresses instead of
-- always falling back to the configured default_to recipient.
-- =============================================================

ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS notify_opt_in BOOLEAN NOT NULL DEFAULT false;