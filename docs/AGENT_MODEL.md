# Uniform Agent Model

## One binary everywhere

Every machine runs the exact same `porter-agent` binary:

```text
first host      -> porter (server) + porter-agent
additional host -> porter-agent
```

There is no separate `worker-agent`, `control-plane-agent`, or `fleet-agent`.

The agent discovers host capabilities:

```text
arch            amd64 | arm64
kvm             true/false
firecracker     version
jailer          available/version
cgroups         v1/v2
bridge          available
disk_free_mib
cpus
memory_mib
labels          operator-defined
```

Porter distinguishes nodes by:

- identity
- labels
- capacity
- health
- optional taints (later)

Not by a different agent binary.

## Registration flow

```text
1. Operator creates a one-time join token in Porter.
2. porter-agent starts with --server and --token.
3. Agent generates local Ed25519 keypair.
4. Agent calls POST /v1/fleet/enroll.
5. Porter validates token, stores node, signs client cert.
6. Agent opens outbound mTLS WebSocket.
7. Agent sends hello + heartbeat.
8. Node appears in Fleet dashboard as ready.
```

The first host can use a local bootstrap token because Porter and the agent are
installed together. After that, every node uses the same join flow.

## Local vs remote transport

Same binary, same protocol, different transport:

```text
same host:  agent -> unix socket / 127.0.0.1 -> porter
remote:     agent -> outbound mTLS WSS:443 -> porter
```

The agent's command/result protocol is identical.

## Agent lifecycle

```text
installed
  -> preflight
  -> enroll
  -> ready
  -> heartbeat
  -> receive commands
  -> execute
  -> report result
  -> reconnect forever
```

It must survive:

- network loss
- Porter restart
- agent restart
- host reboot
- temporary command backlog

## What the agent owns

- local SQLite actual state
- Firecracker processes
- systemd VM units
- TAP/bridge/NAT
- volumes/snapshots
- serial logs
- local metrics

## What Porter owns

- global desired state
- node registry
- scheduler
- operations
- project/deployment/preview/replica records
- API/UI

Agent reports actual state. Porter reconciles desired state.
