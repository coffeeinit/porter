# Porter — MicroVM compute, simplified.

> **The missing control plane.**
>
> Turn bare-metal servers, cloud VMs, and private infrastructure into a simple,
> automated, MicroVM-native application and hosting platform.

Porter is a self-hosted infrastructure control plane implemented primarily in Go.
Its fundamental workload isolation boundary is the Firecracker MicroVM. Porter
combines a developer-oriented PaaS experience with infrastructure administration,
resource enforcement, hosting operations, observability, security, billing,
automation, and AI-assisted operations.

## Product thesis

**Simple outside. Sophisticated inside.**

A normal user should be able to:

```text
Connect GitHub
    ↓
Select repository
    ↓
Porter detects the application
    ↓
Review configuration
    ↓
Deploy
    ↓
Porter builds, provisions, networks, secures and operates it
```

The platform internally performs:

```text
Intent
  ↓
Plan
  ↓
Authorization / Policy / Quota
  ↓
Desired State
  ↓
Scheduling
  ↓
Provisioning
  ↓
Firecracker MicroVM
  ↓
Network + Storage + Gateway + DNS + TLS
  ↓
Health / Observability
  ↓
Reconciliation
  ↓
Usage / Billing / Audit
```

## What Porter is

Porter is one coherent control plane for:

- MicroVM lifecycle
- application deployments
- Git-based builds
- OCI artifact handling
- node management
- scheduling and reconciliation
- networking and IPAM
- gateway and load balancing
- domains, DNS and certificates
- persistent volumes
- backup and restore
- observability
- security and policy
- customers, organizations and teams
- hosting plans, quotas and entitlements
- subscriptions, metering and billing
- workflows, events and webhooks
- marketplace/extensions
- AI-native operations
- CLI, API and infrastructure automation

## What Porter is not

Porter is not initially:

- a Kubernetes distribution
- a Docker management UI
- a Docker replacement
- a hyperscale cloud clone
- a generic hypervisor
- a complete AWS replacement
- a container orchestration wrapper
- a copy of cPanel, WHMCS, Vercel, Railway, Render, Coolify, Dokploy or CapRover

Those products are references for useful capabilities and interaction patterns, not implementation dependencies.

## Core architecture

```text
                         PORTER
                            │
             ┌──────────────┴──────────────┐
             │                             │
        Porter Cloud                  Porter Admin
        Customer UI                  Provider Console
             │                             │
             └──────────────┬──────────────┘
                            │
                       Porter API
                            │
       ┌────────────────────┼────────────────────┐
       │                    │                    │
 Resource Model       Control Plane         Event System
       │                    │                    │
       │          Scheduler / Controllers       │
       │          Reconciler / Workflows        │
       │          Policy / IAM / Billing        │
       └────────────────────┼────────────────────┘
                            │
                       PostgreSQL
                            │
                       Porter Agent
                            │
              ┌─────────────┼─────────────┐
              │             │             │
         Firecracker      BuildKit      Linux/KVM
              │             │             │
              └─────────────┼─────────────┘
                            │
                         MicroVMs
                            │
             ┌──────────────┼──────────────┐
             │              │              │
            Apps           DBs          Workers/Jobs
```

Logical components should normally live in one primary Porter binary/process
where practical. Components are architectural boundaries, not a requirement to
create a daemon for every subsystem.

## Runtime boundary

OCI/Docker images are input and artifact formats. They are not the customer
runtime.

```text
Git / OCI / VM image
        ↓
Build / Import
        ↓
Artifact
        ↓
Guest filesystem + runtime preparation
        ↓
Firecracker MicroVM
        ↓
Workload process
```

Porter must maintain an explicit guest/runtime boundary so that an OCI image is
not incorrectly treated as a bootable VM.

## Data ownership

```text
PostgreSQL
  = authoritative durable control-plane state

Redis
  = optional cache / acceleration / coordination

Porter
  = desired state + orchestration + reconciliation

Firecracker
  = actual MicroVM runtime

Linux
  = kernel, KVM, networking and storage primitives

BuildKit
  = build execution
```

Critical state must never depend solely on Redis.

## Resource model

```text
Platform
 ├─ Providers
 │   └─ Regions
 │       └─ Zones
 │           └─ Node Pools
 │               └─ Nodes
 │                   └─ MicroVMs
 │                       └─ Workloads
 │                           ├─ Deployments
 │                           ├─ Networks
 │                           ├─ Volumes
 │                           ├─ Domains
 │                           └─ Observability
 │
 └─ Customers
     └─ Organizations
         └─ Teams
             └─ Projects
                 └─ Environments
                     └─ Services
                         └─ Subscriptions / Usage
```

The infrastructure graph and business graph are separate dimensions that are
cross-linked by resource IDs and relationships.

## Universal resource contract

Every major resource should expose:

```text
metadata
spec
status
conditions
generation
observedGeneration
owner/dependencies
labels
annotations
events
relationships
permissions
usage
```

`spec` is desired state. `status` is observed state.

## Reconciliation

Porter is declarative internally:

```text
Desired State
     ↓
Observe Actual State
     ↓
Calculate Diff
     ↓
Plan
     ↓
Authorize
     ↓
Act
     ↓
Verify
     ↓
Update Status
     ↓
Repeat
```

Operations must be idempotent. Process crashes, duplicate requests, delayed
events and agent reconnects must not corrupt the resource model.

## Deployment flow

```text
GitHub
  ↓
Webhook / Manual Trigger
  ↓
Source Retrieval
  ↓
Application Detection
  ↓
Build Plan
  ↓
BuildKit
  ↓
OCI Artifact
  ↓
Artifact Validation / Security Checks
  ↓
Runtime Preparation
  ↓
Admission / Quota
  ↓
Scheduler
  ↓
Volume / Network Allocation
  ↓
Firecracker MicroVM
  ↓
Health Checks
  ↓
Gateway Registration
  ↓
DNS / Certificate
  ↓
Traffic Activation
  ↓
Healthy
```

The UI must show these states rather than a generic spinner.

## Networking

Porter owns the networking abstraction while using Linux primitives underneath.

```text
Internet
   ↓
DNS
   ↓
Edge / Cloudflare (optional)
   ↓
Porter Gateway
   ↓
Load Balancer
   ↓
Network Policy
   ↓
MicroVM
   ↓
Workload
```

Networking includes:

- IPAM
- private networks
- public networks
- IPv4/IPv6
- TAP/bridge integration
- routing
- NAT
- ingress/egress policy
- security groups
- service discovery
- internal DNS
- bandwidth accounting
- gateway routing
- TCP/UDP/HTTP/HTTPS/WebSocket

Cloudflare is a first-class integration for DNS, edge security, CDN, WAF,
DDoS protection and related capabilities, but is not a hard Porter dependency.

## Storage

Storage must be split into distinct abstractions:

```text
Ephemeral
  → VM root filesystem / scratch

Persistent Block
  → application data / databases / VM disks

Object Storage
  → artifacts / uploads / backups / exports

Backup Storage
  → disaster-recovery copies
```

Porter should expose logical storage classes rather than forcing users to
understand filesystems and physical devices.

Object storage is provider-neutral. S3-compatible services and customer-managed
MinIO are supported through an `ObjectStore` abstraction.

```text
Porter
  ↓
ObjectStore
  ├─ AWS S3
  ├─ Cloudflare R2
  ├─ Backblaze B2
  ├─ Wasabi
  ├─ Hetzner Object Storage
  └─ Customer MinIO
```

MinIO is not a mandatory hidden dependency.

## Built-in services

Porter should expose common application infrastructure through a consistent
service model:

```text
Database
Object Storage
Cache
Queue
Email
OTP / Verification
Cron / Scheduler
Webhooks
Search
Log Analytics
Secrets
```

A service should be native when it is broadly useful, deeply integrated with
Porter lifecycle/security/billing/observability, and has a stable abstraction.
Specialized external systems belong in extensions or marketplace packages.

## Commerce

Porter is an infrastructure-commerce control plane.

```text
Customer
  ↓
Product
  ↓
Plan
  ↓
Entitlements
  ↓
Subscription
  ↓
Service
  ↓
Usage Events
  ↓
Meters
  ↓
Charges
  ↓
Invoice
  ↓
Payment
  ↓
Subscription State
  ↓
Provisioning / Restriction Workflow
```

Usage must be traceable from invoice line to resource to infrastructure.

Supported concepts include:

- fixed recurring pricing
- usage pricing
- tiered and graduated pricing
- package allowances
- commitments
- add-ons
- credits
- coupons
- taxes
- proration
- trials
- prepaid/postpaid models
- spending limits
- dunning
- refunds and disputes

Billing state must not directly terminate a workload. It emits policy-driven
service actions that go through the normal provisioning workflow.

## Kubernetes capability strategy

Porter adopts useful control-plane primitives without adopting Kubernetes as
its architecture.

```text
Kubernetes                 Porter
Pod                    →   MicroVM / workload instance
Deployment             →   Application
StatefulSet            →   Stateful Service
DaemonSet              →   Node Workload
Job                    →   Batch Job
CronJob                →   Scheduled Job
Service/Ingress        →   Service / Gateway
CNI                    →   Porter Network Controller
CSI                    →   Porter Volume Interface
HPA                    →   Porter Autoscaler
RBAC                   →   Porter IAM
ResourceQuota          →   Porter Quotas
NetworkPolicy          →   Porter Network Policy
CRD/Operators          →   Porter Extensions / Controllers
```

Porter is not Kubernetes-compatible and should not expose Kubernetes-specific
objects such as Pods, CNI configuration, CSI configuration, or namespace-heavy
UX as the primary user model.

## AI operations

AI is a governed operator.

```text
User Intent
  ↓
Agent Context
  ↓
Inspect
  ↓
Plan
  ↓
Policy / Permissions
  ↓
Approval if required
  ↓
Porter API
  ↓
Execute
  ↓
Verify
  ↓
Audit
```

AI uses the same API and authorization model as humans. There is no hidden
superuser AI API.

AI tools are typed resource operations, not arbitrary shell execution.

## Safety model

Automation classes:

```text
READ
  → automatic

LOW-RISK CHANGE
  → policy controlled

PRODUCTION CHANGE
  → policy / approval

DESTRUCTIVE ACTION
  → explicit approval by default
```

Every important operation is observable and auditable.

## Product UX

**UI reference materials** — see [`imagepromt.md`](imagepromt.md) (repo root) for
ready-to-use **AI image-generation prompts** that render product UI screenshots of
the intended customer surfaces (dashboard, app detail/deploy, network & TLS,
usage & billing). These are a UX moodboard for layout, navigation, status-language,
and density. Couple them with the plain-text WHMCS `templates/*.tpl` study under
`references/whmcs-906/` (readable UI layer) as a second, real-world UI reference.

The product follows:

**Apple-level simplicity × Zerodha-level precision × Zoho-level operational breadth**

These are product qualities, not visual-copying requirements.

Three experience levels:

```text
Level 1: Intent
  "Deploy my API"

Level 2: Guided control
  runtime / scaling / domain / resources

Level 3: Infrastructure
  MicroVM / scheduler / network / storage / node / policy
```

Common operations should approach three meaningful interactions:

```text
Create Application
 → Choose Source
 → Review
 → Deploy

Add Domain
 → Enter Domain
 → Confirm
 → Done

Scale
 → Choose Replicas
 → Review Impact
 → Apply
```

## Porter Admin

Porter Admin unifies business and infrastructure administration.

```text
Customers
Organizations
Users
Resellers
Products
Plans
Subscriptions
Billing
Invoices
Payments
Usage
Domains
Support
Providers
Regions
Zones
Nodes
Node Pools
VMs
Deployments
Networks
Storage
Gateway
DNS
Certificates
Observability
Security
Automation
Audit
```

The same control plane serves:

- platform administrators
- infrastructure operators
- billing operators
- support agents
- resellers
- customer administrators
- developers
- auditors
- AI agents

RBAC controls visibility and actions.

## Porter Doctor

`porter doctor` and its UI equivalent provide deterministic diagnostics for:

```text
Node
KVM
Firecracker
BuildKit
Kernel
Networking
Storage
DNS
TLS
Gateway
PostgreSQL
Scheduler
Capacity
Agent
Control Plane
```

AI may consume Doctor output, but Doctor must remain useful without AI.

## Development order

### Phase 1 — Runtime foundation

Go, PostgreSQL, systemd, Firecracker, KVM, basic networking, node bootstrap,
VM lifecycle, persistence and reconciliation.

### Phase 2 — Build and deploy

GitHub integration, BuildKit, OCI artifacts, artifact storage, deployment
controller, logs and health checks.

### Phase 3 — Public application platform

Gateway, domains, DNS, TLS, HTTP routing, WebSocket and load balancing.

### Phase 4 — Multi-node orchestration

Node manager, scheduler, capacity, placement, drain, replicas, replacement and
migration/redeploy.

### Phase 5 — Platform services

Environments, secrets, volumes, autoscaling, deployment strategies, rollback,
databases, object storage, queues and email.

### Phase 6 — Operations

Metrics, logs, traces, events, alerts, incidents, SLOs, topology and synthetic
monitoring.

### Phase 7 — Commercial platform

Customers, products, plans, subscriptions, entitlements, metering, invoices,
payments, quotas, resellers, support and marketplace.

### Phase 8 — AI

Agent identity, permissions, infrastructure search, diagnosis, planning,
remediation, incident analysis and natural-language operations.

## Release path

```text
v0.1.0-beta
v0.2.0
v0.3.0
v0.4.0
v0.5.0
v0.6.0
v0.7.0
v0.8.0
v0.9.0
v0.10.0
v1.0.0-alpha
v1.1.0-final
```

The first vertical slice is more important than feature count:

```text
GitHub
 ↓
Build
 ↓
Artifact
 ↓
Guest preparation
 ↓
Firecracker
 ↓
Network
 ↓
Gateway
 ↓
Domain
 ↓
HTTPS
 ↓
Healthy application
```

## Agent implementation rule

An implementation agent must treat `SRS.md` as the normative product requirement
and `ARCHITECTURE_FLOW.md` as the normative architecture/flow contract.

When a requirement is not implemented yet:

- do not fake it;
- mark it planned/experimental;
- preserve the resource model;
- preserve API boundaries;
- preserve desired/actual state;
- add tests;
- document deviations.

The implementation must prefer a small number of coherent Go components over
a daemon zoo.

## Repository contract

Recommended high-level structure:

```text
porter/
├── cmd/
│   ├── porter/
│   └── porter-cli/
├── internal/
│   ├── api/
│   ├── auth/
│   ├── rbac/
│   ├── policy/
│   ├── resource/
│   ├── controller/
│   ├── scheduler/
│   ├── runtime/
│   ├── firecracker/
│   ├── agent/
│   ├── network/
│   ├── gateway/
│   ├── dns/
│   ├── certificate/
│   ├── storage/
│   ├── build/
│   ├── deployment/
│   ├── observability/
│   ├── billing/
│   ├── workflow/
│   ├── incident/
│   ├── marketplace/
│   └── ai/
├── migrations/
├── web/
├── api/
├── configs/
├── scripts/
├── tests/
├── SRS.md
├── ARCHITECTURE_FLOW.md
└── README.md
```

This structure is illustrative. The architectural boundaries and contracts are
more important than exact directories.

## Quality invariant

> Every complex infrastructure capability must have a simple default path,
> precise information, clear explanation, safe automation, and an explicit
> advanced escape hatch.
