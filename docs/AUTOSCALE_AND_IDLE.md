# Autoscaling, Scale-to-Zero, and Durable Reconcile

## App policy

Each app stores:

```text
autoscale_enabled
min_replicas
max_replicas
cpu_target
idle_timeout_sec
scale_to_zero
last_activity_at
last_cpu_percent
```

API:

```text
GET  /v1/apps/{id}/autoscale
PUT  /v1/apps/{id}/autoscale
POST /v1/apps/{id}/activity
POST /v1/apps/{id}/wake
POST /v1/apps/{id}/scale
```

MCP tools:

```text
porter_get_autoscale
porter_set_autoscale
porter_touch_app
porter_wake_app
porter_scale_app
```

## Controller loop

Porter runs a 5-second reconcile loop:

```text
if enabled and cpu > target and replicas < max:
    replicas += 1
if enabled and cpu < target/2 and replicas > min:
    replicas -= 1
if scale_to_zero and idle > timeout and replicas > 0:
    replicas = 0
```

Traffic/activity is reported with `POST /v1/apps/{id}/activity`.
The gateway will call this automatically later; for now the UI/API/MCP can.

## Wake-up

`POST /v1/apps/{id}/wake` sets desired replicas to at least `min_replicas` or 1.
The scheduler creates replacement replicas and the agent boots them.

## Durable/reliable behavior

- Every scale action goes through the operation ledger.
- Commands are idempotent by operation ID.
- Stale `sent` commands are requeued after 90 seconds.
- Agent local SQLite survives restarts.
- Porter SQLite survives restarts.
- Reconciliation repairs desired/actual drift.

## Next

- gateway request tracking calls `activity` automatically
- per-VM CPU metrics drive autoscale instead of app-reported CPU
- suspend instead of stop for faster wake
- per-replica idle tracking
