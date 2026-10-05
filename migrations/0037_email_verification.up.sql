-- =============================================================
-- 0037_email_verification: per-user verified-email flag plus a
-- single-use verification token ledger.
--   users.email_verified  — flipped by POST /auth/verify-email
--   email_verifications   — hash-only tokens (plain token is shown
--                           once and never stored); expiry + used_at
--                           make consumption single-use and time-bound.
-- =============================================================

ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS email_verifications (
    id         BIGSERIAL PRIMARY KEY,
    user_id    TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,            -- sha256 hex of the plain token
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_email_verifications_user ON email_verifications(user_id);
