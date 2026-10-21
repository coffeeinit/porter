# Porter Fleet Protocol — V1 Draft

This is the minimum wire contract between `porter` and `fleet`.

Transport:

- HTTPS for enrollment and token operations
- mTLS WebSocket for the long-lived node channel
- JSON payloads
- One message envelope for every frame

## Envelope

```go
type Envelope struct {
    ID          string          `json:"id"`
    Type        string          `json:"type"`
    NodeID      string          `json:"node_id,omitempty"`
    OperationID string          `json:"operation_id,omitempty"`
    SentAt      time.Time       `json:"sent_at"`
    Payload     json.RawMessage `json:"payload,omitempty"`
}
```

## Enrollment

`POST /v1/fleet/enroll`

Request:

```json
{
  "token": "penr_...",
  "hostname": "metal-01",
  "os": "linux",
  "arch": "amd64",
  "kernel": "6.12.0",
  "firecracker_version": "1.10.1",
  "public_key": "base64-ed25519-public-key",
  "labels": {"region": "eu-west", "pool": "general"}
}
```

Response:

```json
{
  "node_id": "node_...",
  "client_certificate": "-----BEGIN CERTIFICATE-----...",
  "ca_certificate": "-----BEGIN CERTIFICATE-----...",
  "websocket_url": "wss://porter.example.com/v1/fleet/stream"
}
```

Failure cases:

- missing/expired/used token -> `401`
- unsupported architecture -> `422`
- invalid public key -> `400`

## Fleet -> Porter messages

### `hello`

Sent immediately after WebSocket connect.

```json
{
  "agent_version": "1.0.0",
  "firecracker_version": "1.10.1",
  "kernel": "6.12.0",
  "arch": "amd64",
  "cpus": 64,
  "memory_mib": 262144,
  "disk_free_mib": 2800000,
  "labels": {"region": "eu-west", "pool": "general"}
}
```

### `heartbeat`

```json
{
  "cpus_free": 48,
  "memory_free_mib": 196608,
  "disk_free_mib": 2750000,
  "vm_count": 3,
  "load_1m": 4.2,
  "at": "2026-10-18T12:00:00Z"
}
```

### `vm.state`

```json
{
  "vm_id": "vm_...",
  "state": "running",
  "ip": "10.42.1.5",
  "mac": "02:00:00:00:01:05",
  "pid": 4312,
  "error": "",
  "observed_at": "2026-10-18T12:00:01Z"
}
```

### `operation.result`

```json
{
  "state": "succeeded",
  "error": "",
  "detail": "vm booted"
}
```

### `logs`

```json
{
  "vm_id": "vm_...",
  "stream": "serial",
  "lines": ["Booting Linux..."],
  "at": "2026-10-18T12:00:02Z"
}
```

## Porter -> Fleet messages

### `vm.apply`

Declarative desired state. Fleet must create or update the VM to match this.

```json
{
  "vm_id": "vm_...",
  "project_id": "prj_...",
  "replica_id": "rep_...",
  "image": {
    "kernel_url": "https://.../vmlinux",
    "rootfs_url": "https://.../rootfs.ext4",
    "digest": "sha256:..."
  },
  "vcpus": 2,
  "memory_mib": 1024,
  "disk_mib": 5120,
  "network": {
    "mode": "nat",
    "guest_ip": "10.42.1.5",
    "gateway_ip": "10.42.1.1",
    "mac": "02:00:00:00:01:05"
  },
  "volumes": [
    {"id": "vol_...", "mount": "/data"}
  ],
  "env": {"PORT": "8080"},
  "boot_args": "console=ttyS0 reboot=k panic=1 root=/dev/vda"
}
```

### `vm.stop`

```json
{"vm_id": "vm_..."}
```

### `vm.delete`

```json
{"vm_id": "vm_..."}
```

### `snapshot.create`

```json
{"vm_id": "vm_...", "snapshot_id": "snap_..."}
```

### `snapshot.restore`

```json
{"vm_id": "vm_...", "snapshot_id": "snap_..."}
```

## Idempotency rules

- `vm_id` is immutable.
- `vm.apply` may be repeated safely.
- `vm.delete` is idempotent: absent VM = success.
- `operation_id` identifies one logical operation; duplicate results are ignored after first terminal state.
- Fleet persists the last successful desired spec per VM to survive restarts.

## Versioning

Add a `protocol_version` field in `hello` and every envelope in the implementation. V1 accepts only `1`.

## Optional WireGuard overlay fields

WireGuard is optional. The control channel never depends on it.

Enrollment response may include:

```json
{
  "overlay": {
    "enabled": true,
    "hub_endpoint": "porter.example.com:51820",
    "hub_public_key": "base64...",
    "node_address": "10.200.0.7/24",
    "dns": "10.200.0.1"
  }
}
```

Fleet stores this in its local SQLite `node_identity`/overlay config and reports
overlay health separately in `heartbeat`:

```json
{
  "overlay_connected": true,
  "overlay_latency_ms": 18
}
```

If `overlay_connected=false`, the WSS control path continues normally.

## Component path

V1 has one agent-facing transport:

```text
Porter (embedded fleet manager)  <->  Porter agent
       enrollment + mTLS WebSocket
```

Agents connect outbound only to Porter. Porter never calls Firecracker. Agent
connection handling lives in Porter's fleet-manager package.


