# Plan (not built yet): traffic, autoscaling, fleet view, private networking, ingress + TLS

Status: **design only**. Nothing here is in 3.2. Order = what I would build first.

---------------------------------------------------------------------------------------------------

## 1. Per-VM traffic

**Goal:** see, for every microVM, how much traffic it moves and where it goes.

| Source | What it gives | Cost |
|---|---|---|
| Guest agent `/proc/net/dev` (already collected) | rx/tx bytes + rates, per VM | free |
| Firecracker metrics (`net_eth0.rx_bytes_count`, `tx_*`, throttled counters) | what the *VMM* saw, incl. rate-limiter throttling | free (already flushed) |
| Host TAP counters (`/sys/class/net/tapXXXX/statistics`) | ground truth even if the guest lies / agent is down | free, no root |
| Load balancer (already counts per backend) | connections per replica | free |
| conntrack / nftables counters per VM IP (phase 2) | top peers / ports, inbound vs outbound split | small root-helper command `net-flows VID` |

**Storage:** one SQLite table `samples(vm_id, ts, cpu, mem, rx, tx, rd, wr, conns)` written by a collector thread every
10 s for running VMs, rolled up to 1-minute rows after 6 h and 15-minute rows after 7 days (ring buffer, ~3 MB per VM per month).

**UI:** VM → *Networking* gets a traffic chart (in/out, throttled events marked), "top peers" table (phase 2),
and the existing live charts get a **1 h / 24 h / 7 d** range selector (today they show the last ~3 minutes, live only).

**Risks:** counters reset when a VM restarts (store deltas, detect resets); guest-reported numbers are untrusted
(show TAP counters as the authority).

---------------------------------------------------------------------------------------------------

## 2. Autoscaling (how the load balancer "triggers" new machines)

**The important point:** a load balancer only *spreads* connections. It never creates machines.
Something else has to decide "we need more replicas". That something is a small **autoscaler loop**
that already has everything it needs: replica sets can scale (`scale(n)`) and the balancer already
picks up new healthy replicas on the next connection because it reads the replica list on every connection.

```
 every 15 s, per replica set that has autoscale enabled:

   metrics  ──►  decision  ──►  action
   ---------     --------      ------
   avg CPU % of ready replicas          desired = ceil(current * metric / target)
   (optional) active LB connections     clamp to [min, max]
   per replica                          scale UP   if over target for  60 s   (reacts fast)
                                        scale DOWN if under target for 5 min   (reacts slow)
                                        cooldown 2 min after any change
        │
        ▼
   replicas.scale(set, desired)  ──►  new VM boots ──► agent answers ("ready")
        │                                                      │
        └── LB starts sending connections to it only once it is ready ◄┘
```

**Settings per set:** `min`, `max`, `target CPU %` (default 60), optional `target connections per replica`,
scale-up/down windows, cooldown. Defaults are conservative; autoscale is **off** unless enabled.

**Scale-down without dropping users (connection draining):** pick the newest replica → mark it *draining*
(LB stops giving it **new** connections) → wait until its active connections hit 0 or 60 s → stop and delete it.

**Why boots are fast enough:** a microVM boots in ~1–2 s, but the image's own app may take longer, so "ready"
= agent up **and** (optional) service port accepting TCP. The autoscaler never counts an unready replica as capacity.

**Guard rails:** hard cap `max`, per-host VM limit (`MVM_MAX_VMS`), refuse to scale up if host memory
left < one replica, everything written to the Activity log ("scaled web 2→3: avg CPU 84% > 60%").

**What it does not do:** scale the *host* itself. This is single-host; running out of host RAM/CPU is a hard stop
(it says so). Multi-host is a separate project (see "Later").

---------------------------------------------------------------------------------------------------

## 3. Fleet view (per-machine detail, logs, metrics — essential when self-hosted)

One page, **Fleet**, built on the collector from section 1 (so no new data source):

* **Table of every machine** (replicas and standalone): status, set, image, vCPU/RAM, CPU %, mem %, net in/out,
  disk, uptime, restarts (auto-heal count), last error. Sort, filter (status / set / image / "unhealthy"), search.
* **Aggregates on top:** total running, total vCPU/RAM allocated vs host capacity, fleet-wide CPU/net sparkline,
  "needs attention" count (errors, failed heals, drifting rollouts).
* **Click a machine → side drawer** with tabs: *Metrics* (1 h/24 h/7 d charts), *Logs*, *Events*.
* **Logs:** console (`console.log`) + Firecracker log (already filtered) + workload output, in one stream with
  source badges, text search, level filter, "follow" mode. Per-VM ring buffer on disk (e.g. 10 MB, rotated).
  Multi-select machines → **merged, time-ordered log** across a replica set.
* **Events timeline:** started / stopped / crashed / healed / scaled / rolled out / snapshot / config change
  (the audit log already records requests; this adds machine-level events from the reconcile + heal loops).
* **Bulk actions:** restart / stop / snapshot / drain on the selection.

**Risks:** log volume (hence caps + rotation); the guest only produces useful logs if the workload writes to stdout —
the init already forwards workload output to the serial console, which is what `console.log` captures.

---------------------------------------------------------------------------------------------------

## 4. Private networking per project, ingress and TLS

Terminology: a **workspace** (project) groups machines that belong together.

### 4a. Network tiers

| Tier | Scope | Default | How |
|---|---|---|---|
| **Private** | machines in the same workspace | **on** | one Linux bridge per workspace (`mvm-<ws>`, its own /24 from 10.<n>.0.0/16 pool); isolation between workspaces enforced in the existing `MVM-FWD` chain |
| **Published** | one host port, through the replica-set load balancer | off | per-service "expose" toggle (the LB / port-forward code already exists) |
| **Public** | internet | off | Published + DNS + TLS through the ingress; DBs get a loud warning and need an explicit confirm |

### 4b. "App and DB talk by name, no IPs, no config"

* A tiny **DNS responder** inside the app (UDP/TCP 53 bound only on each workspace bridge's gateway IP) answers
  `<service>` and `<service>.<workspace>.internal` with that workspace's machine IPs (all replicas → round-robin A records).
  Everything else is forwarded to the upstream resolvers.
* The guest init already writes `/etc/resolv.conf` from the kernel command line → it will point at the bridge gateway, so
  **no change inside images**. Search domain `<workspace>.internal` added the same way, so plain `db` resolves.
* Machines in other workspaces cannot resolve or reach them (separate bridge + firewall rule + DNS view per workspace).
* Replica sets resolve as one name; individual replicas as `web-1`, `web-2`.

### 4c. Ingress with TLS per service

* **Traefik** (single static binary, started and supervised by the app as a normal host process, config generated, not hand-edited).
  Routes come from a **file provider** the app rewrites atomically whenever services/replicas change; Traefik hot-reloads it.
* Router per exposed service → load-balances across the service's ready replicas (replaces the user-space TCP balancer for HTTP;
  the TCP balancer stays for non-HTTP like databases).
* **Certificates:** Let's Encrypt ACME built into Traefik. HTTP-01 / TLS-ALPN for single hostnames (on demand), DNS-01 for wildcards.
* Host needs ports 80/443 free and reachable; the privileged helper only gains a rule to allow them in the firewall.

### 4d. Domains — two modes

| Mode | When | What happens |
|---|---|---|
| **User domain** | user enters `app.example.com` and points DNS at the host's IP | route added; cert issued on first request; the UI shows the DNS record to create and a live "DNS ok / cert issued" check |
| **Auto domain** | no domain given | `<service>.<workspace>.<yourdomain>` under a wildcard cert you control (DNS-01 with a supported DNS provider), **or** the sslip.io style `<service>-<workspace>.<host-ip-dashed>.sslip.io` for zero-DNS setups |

**Honest caveats to decide on before building:**
* **sslip.io + HTTPS:** every name is under one shared registrable domain, so Let's Encrypt's per-domain rate limits apply
  to *everyone* using it — fine for a handful of hosts, unreliable at scale. Plan: sslip.io = default for **HTTP / quick start**,
  HTTPS on sslip.io best-effort, and recommend a real domain with a wildcard cert for anything serious.
* A wildcard cert needs DNS-01, i.e. an API token for the user's DNS provider (stored encrypted at rest, never shown after saving).
* Public exposure of databases is blocked by default; enabling it requires typing the service name.

---------------------------------------------------------------------------------------------------

## Suggested build order

1. Metrics collector + SQLite samples + range selector on the existing charts  (unblocks 2 and 3)
2. Fleet page (table, aggregates, drawer, logs, events)
3. Autoscaler + connection draining
4. Per-VM traffic extras (TAP counters, conntrack top peers)
5. Workspaces + per-workspace bridge + internal DNS (private networking)
6. Traefik ingress + user domains + TLS
7. Auto domains (sslip.io first, wildcard DNS-01 second)

Each step ships on its own and has tests; 1–3 need no new privileged code, 5–6 add a few narrow helper commands.

## Later / out of scope for now
Multi-host scheduling, shared volumes between machines, GPU/USB passthrough (not supported by Firecracker), billing.
