# Fleet ↔ Porter Connectivity

## Recommendation

Use **two layers**:

1. **Control layer (required):** Fleet initiates an outbound mTLS WebSocket to
   Porter on TCP 443.
2. **Overlay layer (optional):** Porter acts as a WireGuard hub for private
   node networking and management traffic.

This gives reliable control-plane connectivity behind NAT while still allowing
a private encrypted network between nodes when WireGuard is available.

> **Status:** The outbound mTLS WebSocket described here is a target design, not the current transport. Current agents use HTTPS request/response polling; optional operator-managed mTLS is available on agent fleet routes. See [`PROTOCOL.md`](PROTOCOL.md) for the implemented behavior.

## Why not WireGuard only?

WireGuard is excellent for encrypted point-to-point links, but it is not a
NAT-traversal or relay protocol. If a node has no public endpoint and is behind
symmetric NAT, WireGuard alone may not connect. The outbound WebSocket remains
the reliable baseline.

## Control connection

```text
Porter agent
  -> DNS lookup porter.example.com
  -> TCP:443 TLS with client cert
  -> WebSocket upgrade
  -> hello
  -> heartbeat every 10s
  -> receive commands
  -> report operation progress/results
```

Features:

- exponential reconnect backoff
- jitter to avoid thundering herd
- ping/pong keepalive
- bounded message queue
- resumable by node ID and operation ID

## Optional WireGuard hub

```text
Porter host
  wg0: 10.200.0.1/24
  UDP 51820

Fleet node A: 10.200.0.2
Fleet node B: 10.200.0.3
```

Porter allocates overlay IPs and distributes peer configuration.

Suggested interface:

```go
type OverlayManager interface {
    EnsureHub(ctx context.Context) error
    AddPeer(ctx context.Context, nodeID string, publicKey string, allowedIPs []string) error
    RemovePeer(ctx context.Context, nodeID string) error
    PeerConfig(ctx context.Context, nodeID string) (WireGuardPeerConfig, error)
}
```

Implementation options:

- Kernel WireGuard: `golang.zx2c4.com/wireguard/wgctrl`
- Userspace fallback: `golang.zx2c4.com/wireguard/device`
- Platform setup remains Linux-only for V1.

## If zero inbound ports are mandatory

Then WireGuard cannot be the only transport. Options:

- run Porter behind a TCP reverse proxy and keep WSS control path
- use an optional DERP/relay service
- integrate Headscale/Tailscale as a separate optional project later

These are target-design options, not current V1 guarantees. The current agent transport is HTTPS request/response polling; no WSS control path is implemented.

## Fleet endpoint

Agents currently connect outbound to Porter's HTTP API:

```text
agent -> porter  (JSON HTTPS polling + bearer node token; optional mTLS)
```

Set `PORTER_API_TLS_CERT`, `PORTER_API_TLS_KEY`, and `PORTER_AGENT_CLIENT_CA`
to enable the optional node-bound mTLS gate. See [`PROTOCOL.md`](PROTOCOL.md)
for route and certificate identity details. WebSocket connection handling is
future work, not part of the current agent protocol.
