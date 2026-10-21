# Fleet Local State

## Why SQLite on every node?

Firecracker has no Docker-like daemon that can list every VM with its full
configuration. A microVM is a process plus:

- kernel path
- rootfs path
- API socket
- TAP device / MAC / IP
- data volumes
- snapshot paths
- environment/metadata

Process scanning cannot reconstruct this safely. The Porter agent must persist it locally in `porter-agent.db`.

## Authority split

| Data | Authority |
|---|---|
| Global desired state | Porter SQLite |
| Scheduler placement | Porter |
| Global node registry | Porter SQLite (embedded fleet manager) |
| Actual local VM inventory | Fleet SQLite |
| Local PID/socket/TAP state | Fleet SQLite + host checks |
| Volumes/snapshots on host | Fleet SQLite |
| Operations on host | Fleet SQLite |

## Recovery sequence

On Porter agent startup:

1. Open local SQLite (`porter-agent.db`).
2. Load recorded VMs and operations.
3. Check each VM's Firecracker API socket and PID.
4. Reconcile:
   - recorded VM + live VMM -> report actual state
   - recorded VM + dead VMM -> report `failed`
   - live VMM + unrecorded VM -> mark orphaned, never delete automatically
5. Connect to Porter and send `hello` + current VM state.
6. Porter reconciles desired state and sends any required commands.

## SQLite settings

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;
```

Use `modernc.org/sqlite` for a CGO-free static binary.

## Local schema sketch

```sql
CREATE TABLE node_identity (
    id TEXT PRIMARY KEY,
    certificate_fingerprint TEXT NOT NULL,
    enrolled_at TIMESTAMP NOT NULL
);

CREATE TABLE vms (
    id TEXT PRIMARY KEY,
    replica_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    desired_json TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE vm_runtime (
    vm_id TEXT PRIMARY KEY REFERENCES vms(id) ON DELETE CASCADE,
    state TEXT NOT NULL,
    pid INTEGER,
    api_socket TEXT,
    tap_name TEXT,
    ip TEXT,
    mac TEXT,
    last_error TEXT,
    observed_at TIMESTAMP NOT NULL
);

CREATE TABLE volumes (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    size_mib INTEGER NOT NULL,
    attached_vm_id TEXT
);

CREATE TABLE snapshots (
    id TEXT PRIMARY KEY,
    vm_id TEXT NOT NULL,
    state_path TEXT NOT NULL,
    memory_path TEXT NOT NULL,
    sha256 TEXT,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    updated_at TIMESTAMP NOT NULL
);
```


## Embedded fleet-manager tables in `porter.db`

Porter's embedded fleet manager stores node connectivity and command state in
the same `porter.db`:

```text
nodes              -- identity, state, labels, capacity snapshot
node_certificates  -- cert serial, fingerprint, issued/expiry
join_tokens        -- hashed one-time tokens, expiry, used_by
heartbeats         -- latest and bounded history
commands           -- queued/sent/acked/failed commands per node
overlay_ips        -- WireGuard address allocation
agent_versions     -- reported versions and upgrade channel
upgrade_jobs       -- signed upgrade/rollback jobs
```

Porter owns global desired state and fleet connectivity state. Each agent DB
holds actual host-local state.


