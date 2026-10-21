-- 0036_vps_workload_type: persistent VPS mode (WP1). A project flipped to
-- workload_type='vps' is persistent — its replica pool never scales to zero
-- and its IP allocations stay pinned instead of being recycled. The columns
-- here are the SQL query surface; project state itself is serialized in
-- projects.data (store.PutProject writes the whole types.Project there), and
-- store.SetProjectWorkloadType moves both in one write. VMs need no columns:
-- their workload fields persist inside replicas.data (store.PutVM
-- serializes the whole types.VM).
ALTER TABLE projects ADD COLUMN IF NOT EXISTS workload_type TEXT NOT NULL DEFAULT 'microvm';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS persistent BOOLEAN NOT NULL DEFAULT FALSE;

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'projects_workload_type_check') THEN
    ALTER TABLE projects ADD CONSTRAINT projects_workload_type_check
      CHECK (workload_type IN ('microvm','vps'));
  END IF;
END $$;

-- Static-IP pinning: ip_allocations (0017) is the durable per-VM allocation
-- record; pinned rows must not be recycled by the allocator and must not be
-- released by ephemeral teardown. This is the store-level half of VPS static
-- IPs — attaching a brand-new static address on a real host is KVM-metal
-- work, planned (SRS §66: never fake unimplemented functionality).
ALTER TABLE ip_allocations ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE;
