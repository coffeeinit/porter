-- =============================================================
-- Multi-node cluster: extend the servers table with node health
-- fields so the control plane can show live status/details for
-- every registered worker and accept heartbeats from them.
-- =============================================================

ALTER TABLE servers ADD COLUMN IF NOT EXISTS address    TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS status     TEXT NOT NULL DEFAULT 'registered';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS vcpus      INT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS mem_mib    INT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS os         TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS arch       TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS version    TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS projects   INT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS vms        INT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS last_seen  TIMESTAMPTZ;
