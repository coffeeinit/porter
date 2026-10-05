-- =============================================================
-- 0017_scoped_rbac_tenancy_compute: bridge 0007 toward scoped capability RBAC
-- (T1a/T1b). DESIGN (binding, see docs/backend-completion-plan.md §2):
-- keep roles/permissions/role_permissions rows; ADD scope-aware assignments
-- + deny effect on the existing mapping. permissions.id IS the capability
-- string. No data migration, no handler rewrites in this phase.
-- Also adds the MVP tenancy + compute/network tables (resellers, teams,
-- ip_allocations, micro_vms). NO billing tables until subscriptions ship.
-- =============================================================

-- Deny-overrides-allow on the existing role→permission mapping.
ALTER TABLE role_permissions ADD COLUMN IF NOT EXISTS effect TEXT NOT NULL DEFAULT 'allow';

-- The RBAC edge: who holds which role at which scope. Grants inherit downward
-- (platform → reseller → org → team → project → environment).
CREATE TABLE IF NOT EXISTS role_assignments (
    id             BIGSERIAL PRIMARY KEY,
    principal_type TEXT NOT NULL,             -- user | service_account | agent
    principal_id   TEXT NOT NULL,             -- username / service-account id / agent id
    scope_type     TEXT NOT NULL,             -- platform | reseller | org | team | project | environment
    scope_id       TEXT NOT NULL DEFAULT '',  -- '' with scope_type='platform' = whole platform
    role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_by     TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (principal_type, principal_id, scope_type, scope_id, role_id)
);
CREATE INDEX IF NOT EXISTS idx_role_assignments_principal ON role_assignments(principal_type, principal_id);
CREATE INDEX IF NOT EXISTS idx_role_assignments_scope ON role_assignments(scope_type, scope_id);

-- Tenancy: reseller (optional capacity-reselling entity) + team (sub-division
-- bridging orgs→projects; complements existing orgs/groups/projects).
CREATE TABLE IF NOT EXISTS resellers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS teams (
    id              TEXT PRIMARY KEY,
    organization_id TEXT,
    reseller_id     TEXT REFERENCES resellers(id) ON DELETE SET NULL,
    name            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_teams_org ON teams(organization_id);

-- Networking: durable IP allocations backing the per-VM allocator (T6).
CREATE TABLE IF NOT EXISTS ip_allocations (
    id          BIGSERIAL PRIMARY KEY,
    network_id  TEXT,
    ip          INET,
    micro_vm_id TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (network_id, ip)
);
CREATE INDEX IF NOT EXISTS idx_ip_allocations_vm ON ip_allocations(micro_vm_id);

-- Compute: MicroVM runtime truth (replica_id → replicas pool, node_id → servers).
CREATE TABLE IF NOT EXISTS micro_vms (
    id            TEXT PRIMARY KEY,
    replica_id    TEXT,
    node_id       TEXT,
    state         TEXT NOT NULL DEFAULT 'provisioning',
    cpu_millicores INT NOT NULL DEFAULT 1000,
    mem_mib       INT NOT NULL DEFAULT 512,
    target_state  JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_micro_vms_replica ON micro_vms(replica_id);
CREATE INDEX IF NOT EXISTS idx_micro_vms_node ON micro_vms(node_id);
