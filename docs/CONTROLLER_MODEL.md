# Controller and Scheduling Model

## 1. Universal resource fields

Every managed resource should expose:

```text
id
spec                 -- desired state
status               -- observed state
conditions[]         -- typed reasons
generation           -- increment when spec changes
observed_generation  -- last generation reconciled
created_at
updated_at
```

Resources:

```text
node
project
app
deployment
preview
replica
microvm
volume
domain
route
```

## 2. Desired state

Example replica spec:

```yaml
replica:
  app: edge-api
  deployment: dep_123
  replicas_desired: 3
  resources:
    vcpus: 2
    memory_mib: 1024
    disk_mib: 5120
  network:
    mode: nat
  health:
    type: http
    path: /health
    port: 8080
```

Rules:

- API persists desired state, returns an operation ID.
- Controllers never act inside the request handler.
- Desired state is immutable per deployment where required.
- Scaling only changes `replicas_desired`.

## 3. Reconciliation

Loop:

```text
observe actual
  -> compare desired vs actual
  -> plan safe actions
  -> authorize
  -> dispatch to agent
  -> verify
  -> update status/conditions
  -> repeat
```

Requirements:

- idempotent by `operation_id` and resource ID
- exponential backoff on failure
- never assume an API acknowledgement means success
- never delete resources automatically on transient errors
- every transition emits an event
- report explicit conditions, never only "failed"

Suggested condition types:

```text
Scheduled
AgentReachable
ImageReady
VolumeReady
NetworkReady
Booted
Healthy
TrafficActive
```

## 4. Scheduling

Filter:

- node ready, not cordoned/draining
- sufficient CPU, memory, disk
- architecture matches image
- required labels match
- volume affinity satisfied
- GPU/resources if requested later

Score:

- least allocated first
- most free memory
- image cache hit
- stable node ID for reproducibility

Persist placement before sending `vm.apply`.

## 5. Cordon and drain

Cordon:

- mark node unschedulable
- existing VMs continue

Drain:

- set node `draining`
- stop placing new replicas
- evict replicas one at a time
- respect disruption budget
- reschedule if capacity exists
- if no capacity, keep VM and surface `DrainBlocked` condition
- never kill a workload just to finish drain

## 6. Rolling upgrades

For deployment N -> N+1:

1. create one new replica on target capacity
2. wait for healthy
3. add to traffic
4. remove one old replica from traffic
5. stop old replica
6. repeat until complete

Rules:

- maximum unavailable / maximum surge configurable
- auto-abort and rollback on health failure
- never route traffic to unready replicas
- preview deployments are separate and do not affect production

## 7. Health checks

Types:

```text
http
tcp
exec (requires guest agent)
```

Probes:

- startup/readiness/liveness
- interval, timeout, healthy threshold, unhealthy threshold

Failure handling:

- mark condition `Healthy=False`
- remove from gateway rotation
- for `restart_policy=always`: restart/replace via agent
- for `on-failure`: restart with backoff
- emit event and update operation
- cap restart loop and surface `CrashLoopBackOff`-like condition

## 8. Agent operation contract

Porter sends commands; agent executes and reports:

```text
command.queued
command.sent
command.acked
vm.provisioning
vm.booting
vm.running
vm.healthy
vm.failed
command.succeeded
command.failed
```

Agent local SQLite is required to survive restarts and replay idempotently.
