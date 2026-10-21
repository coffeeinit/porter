# Porter — Software Requirements Specification

Version: 1.0 consolidated working specification  
Status: Normative product and engineering requirements  
Primary language: Go  
Primary isolation/runtime: Firecracker MicroVM  
Primary durable state: PostgreSQL  
Architecture style: declarative control plane + controllers + reconciliation

## 1. Purpose

This SRS defines the consolidated functional, non-functional, architectural,
operational, security, UX, API, commercial and implementation requirements for
Porter.

It supersedes the earlier ideology-only SRS as the working engineering contract.
The architecture document remains the detailed execution/flow companion.

The specification is deliberately written so an implementation agent can derive
work items, interfaces, schemas, tests and acceptance criteria without inventing
a second product architecture.

## 2. Product definition

Porter SHALL provide a self-hosted control plane that turns physical servers,
cloud VMs and private infrastructure into a managed MicroVM-native application
and hosting platform.

Porter SHALL combine:

1. PaaS deployment workflows.
2. MicroVM workload isolation.
3. Infrastructure orchestration.
4. Hosting-provider administration.
5. Networking, DNS and gateway management.
6. Storage and recovery.
7. Observability and incident operations.
8. IAM, policy and security.
9. Usage metering and commercial lifecycle management.
10. Automation, workflows and AI operations.

Porter SHALL remain useful on a single server and scale conceptually to
multi-node/private-cloud installations.

## 3. Goals

Porter SHALL:

- use Firecracker as the primary workload isolation boundary;
- be implemented primarily in Go;
- own the logical control plane;
- avoid requiring Kubernetes;
- avoid requiring Docker as the customer runtime;
- support Git-based deployment;
- support OCI artifact inputs;
- manage MicroVM lifecycle;
- enforce CPU, memory, storage and other resource constraints;
- support replicas and reconciliation;
- provide networking and gateway capabilities;
- provide domain/DNS/TLS management;
- provide persistent storage;
- provide backups and restore;
- provide operational observability;
- provide tenant isolation and RBAC;
- provide an API consumed by UI, CLI, automation and AI;
- support commercial plans, quotas, subscriptions and usage;
- expose advanced infrastructure controls without making them mandatory.

## 4. Non-goals

Porter SHALL NOT initially attempt to become:

- a Kubernetes distribution;
- a Kubernetes-compatible platform;
- a general-purpose hyperscale cloud;
- a Docker replacement;
- a hypervisor;
- an operating system;
- a complete AWS replacement;
- a billing company independent of Porter infrastructure;
- a generic infrastructure-as-code framework.

External integrations may be added without redefining the core architecture.

## 5. Normative language

`MUST` / `SHALL` = mandatory.  
`SHOULD` = required unless a documented engineering reason exists.  
`MAY` = optional/future capability.

An implementation agent MUST NOT silently convert a SHOULD into a mandatory
dependency or treat a planned feature as implemented.

## 6. Actors and scopes

### Platform administrator

Full platform control including infrastructure, tenants, policies, products,
plans, billing, security and audit.

### Infrastructure operator

Nodes, capacity, scheduling, networks, storage, workloads, maintenance and
diagnostics.

### Billing operator

Customers, products, plans, subscriptions, invoices, payments, metering,
credits, taxes and dunning.

### Support operator

Customer and service investigation with limited infrastructure access.

### Reseller

A delegated provider that operates customers within an allocated resource pool.

### Organization administrator

Manages organization members, teams, projects, services and resources.

### Developer

Operates projects, deployments, environments, domains, logs, configuration and
approved runtime controls.

### Service account

Non-human identity for automation.

### AI agent

A first-class identity subject to the same authentication, authorization,
policy, workflow and audit system.

### Extension

A separately governed integration or provider adapter with scoped permissions.

## 7. Tenant hierarchy

The logical model SHALL support:

```text
Platform
 ├── Resellers
 │    └── Customers
 └── Organizations
      ├── Users
      ├── Teams
      └── Projects
           └── Environments
                └── Services
                     └── Workloads
```

Infrastructure SHALL remain a separate dimension:

```text
Provider
 └── Region
      └── Zone
           └── Node Pool
                └── Node
                     └── MicroVM
```

Cross-links SHALL connect logical resources to infrastructure resources.

## 8. Resource contract

Every major resource SHALL contain:

```text
metadata
spec
status
conditions
generation
observed_generation
owner/dependencies
labels
annotations
events
relationships
permissions
usage
```

The API SHALL distinguish desired state from observed state.

Status SHALL be authoritative or explicitly identify the telemetry source.

## 9. Authentication and identity

Porter SHALL support:

- password-based authentication;
- secure sessions;
- API keys;
- service accounts;
- machine/node identities;
- agent identities.

Future integrations MAY support OIDC/SAML/external identity providers.

Node enrollment SHALL create a cryptographic identity. Node identity MUST NOT be
permanently derived from public IP or hardware identifiers.

Private credentials MUST NOT be embedded in binaries.

## 10. Authorization

Authorization SHALL apply consistently to:

- web UI;
- API;
- CLI;
- Terraform/infrastructure automation;
- webhooks;
- workflows;
- AI agents;
- extensions.

Authorization scopes SHOULD include:

```text
platform
reseller
customer
organization
team
project
environment
service
deployment
workload
network
storage
billing
```

Sensitive operations SHALL require explicit permissions.

## 11. Policy and admission

Every create/update operation SHALL follow:

```text
Request
 ↓
Authentication
 ↓
Authorization
 ↓
Quota / entitlement check
 ↓
Policy / admission
 ↓
Validation
 ↓
Defaulting / mutation
 ↓
Persist desired state
 ↓
Controller reconciliation
```

Policy MAY enforce:

- resource limits;
- feature entitlements;
- region restrictions;
- node-pool restrictions;
- network policy;
- image policy;
- security policy;
- deployment approvals;
- billing restrictions;
- rate limits;
- maintenance restrictions.

## 12. MicroVM runtime

Firecracker SHALL be the fundamental workload isolation primitive.

Porter SHALL provide a runtime abstraction over Firecracker.

Lifecycle MUST support at least:

```text
Provisioning
Starting
Running
Degraded
Stopping
Stopped
Failed
Recovering
Deleting
Deleted
```

The runtime SHOULD support:

- create;
- configure;
- start;
- stop;
- pause;
- resume;
- reboot;
- delete;
- snapshot;
- restore;
- vCPU;
- memory;
- drives;
- network interfaces;
- kernel configuration;
- metadata;
- diagnostics.

The control plane MUST isolate privileged host operations from ordinary
application logic.

## 13. Guest/runtime model

OCI/Docker images are artifact formats, not automatically bootable MicroVMs.

Porter SHALL define an explicit transformation:

```text
OCI / VM source
 ↓
Artifact
 ↓
Guest filesystem/runtime preparation
 ↓
Kernel + root filesystem
 ↓
Firecracker
 ↓
Workload
```

The implementation MAY use a minimal Porter guest runtime, a containerd-based
guest runtime where required, or a single-process filesystem model for suitable
workloads.

The chosen mechanism MUST preserve MicroVM isolation as the runtime boundary.

## 14. Node management

Node enrollment SHALL validate:

- OS;
- architecture;
- CPU;
- memory;
- storage;
- network;
- KVM;
- cgroups v2;
- Firecracker;
- BuildKit;
- required kernel capabilities.

Node state SHOULD progress through:

```text
REGISTER
 → AGENT_CONNECTED
 → HARDWARE_DISCOVERED
 → KVM_VERIFIED
 → FIRECRACKER_READY
 → BUILDKIT_READY
 → NETWORK_READY
 → STORAGE_READY
 → CAPABILITIES_REGISTERED
 → READY
```

Node operations SHALL include:

- register;
- activate;
- cordon;
- drain;
- maintenance;
- upgrade;
- reboot;
- recover;
- replace;
- remove.

## 15. Node agent

The agent SHALL:

- maintain authenticated connectivity;
- report capabilities and health;
- execute authorized host operations;
- manage Firecracker;
- participate in network configuration;
- participate in storage operations;
- participate in build execution;
- report telemetry;
- support signed upgrades.

Agent upgrades SHALL use:

```text
download
 → signature verification
 → checksum verification
 → stage
 → switch
 → restart
 → health check
 → rollback if unhealthy
```

## 16. Resource management

Porter SHALL distinguish:

```text
physical capacity
allocated capacity
reserved capacity
actual usage
available capacity
```

Resource enforcement SHALL cover at minimum:

- CPU;
- memory;
- storage;
- MicroVM count;
- network/bandwidth where supported;
- process/task limits.

Future resources MAY include GPU, accelerators and I/O limits.

Resource accounting MUST be enforced, not merely displayed.

## 17. Scheduler

Porter SHALL provide its own scheduler.

Scheduling inputs SHOULD include:

- CPU;
- memory;
- architecture;
- storage;
- network;
- region;
- zone;
- node pool;
- labels;
- capabilities;
- taints;
- affinity;
- anti-affinity;
- priority;
- quota;
- cost;
- availability;
- capacity;
- failure domain.

Scheduling strategies MAY include:

- bin packing;
- resource scoring;
- topology spreading;
- GPU-aware placement;
- cost-aware placement;
- failure-domain distribution.

Scheduler decisions MUST be reproducible from persisted resource state and
placement constraints.

## 18. Reconciliation

Controllers SHALL continuously converge actual state toward desired state.

```text
Desired
 ↓
Observe
 ↓
Diff
 ↓
Plan
 ↓
Act
 ↓
Verify
 ↓
Status
 ↓
Repeat
```

Major controllers SHOULD include:

- application;
- deployment;
- workload;
- replica;
- MicroVM;
- node;
- network;
- gateway;
- domain;
- certificate;
- volume;
- backup;
- scheduler;
- autoscaler;
- billing/provisioning;
- incident;
- workflow.

Controller actions MUST be idempotent.

## 19. Failure and retry model

Porter SHALL tolerate and recover from:

- controller process failure;
- API retries;
- duplicate events;
- delayed events;
- agent disconnects;
- MicroVM crashes;
- node failures;
- network failures;
- failed builds;
- failed deployments;
- temporary database unavailability;
- optional Redis failure.

Critical state MUST be recoverable from PostgreSQL and actual infrastructure
observation.

## 20. Workloads

Porter SHOULD support:

- web services;
- APIs;
- workers;
- background workers;
- functions;
- cron;
- scheduled jobs;
- batch jobs;
- private services;
- databases;
- stateful services;
- custom VMs;
- one-shot workloads.

All workloads SHALL reuse common primitives for:

```text
runtime
network
storage
secrets
health
logs
metrics
events
scaling
lifecycle
RBAC
billing
```

## 21. Deployments

Deployment sources SHALL support:

- Git repositories;
- GitHub;
- OCI images;
- supported VM images;
- prebuilt artifacts.

Git flow:

```text
Repository
 ↓
Webhook/manual trigger
 ↓
Source retrieval
 ↓
Application detection
 ↓
Build
 ↓
Artifact
 ↓
Runtime preparation
 ↓
Provision
 ↓
Health
 ↓
Traffic
```

Deployment records SHALL include:

- version;
- source revision;
- status;
- timestamps;
- configuration reference;
- artifact reference;
- health state;
- logs;
- actor/trigger;
- audit information.

## 22. Build system

BuildKit SHALL be the primary build engine.

Build subsystem SHOULD support:

- Dockerfile builds;
- build arguments;
- build secrets;
- layer caching;
- remote cache;
- build workers;
- multi-architecture builds;
- queueing;
- timeout;
- cancellation;
- logs;
- SBOM;
- provenance;
- image signing;
- vulnerability scanning;
- artifact retention.

BuildKit MUST NOT be treated as the customer runtime.

## 23. Deployment strategies

Porter SHOULD support:

- recreate;
- rolling;
- blue/green;
- canary;
- weighted traffic;
- health-gated promotion;
- manual approval;
- automatic rollback;
- deployment freeze.

Production deployment strategies MUST preserve audit history.

## 24. Rollback

A rollback SHALL:

1. identify a known deployment version;
2. resolve the required artifact/runtime;
3. provision or restore the workload;
4. validate health;
5. shift traffic;
6. record the operation.

Rollback SHOULD avoid destroying persistent state.

## 25. Environments

Porter SHOULD support:

- development;
- staging;
- production;
- preview;
- ephemeral;
- custom environments.

Environments MAY define:

- variables;
- secrets;
- policies;
- approvals;
- protection rules;
- domains;
- resource limits;
- scaling;
- deployment strategy;
- retention.

## 26. Secrets

Secrets SHALL be first-class resources.

Secrets MUST:

- be encrypted at rest;
- have controlled access;
- support scoped injection;
- support rotation/versioning where appropriate;
- generate audit events for sensitive operations.

Secret values MUST NOT appear in ordinary logs, traces, events or audit records.

## 27. Networking

Porter SHALL own a provider-neutral network abstraction over Linux primitives.

Capabilities SHOULD include:

- IPAM;
- IPv4;
- IPv6;
- private/public networks;
- TAP devices;
- bridges;
- routing;
- NAT;
- internal DNS;
- service discovery;
- ingress/egress policy;
- security groups;
- firewall;
- bandwidth limits;
- bandwidth accounting.

The network subsystem MUST validate host privileges before creating MicroVM
interfaces.

## 28. Gateway

The gateway SHALL provide:

- HTTP/HTTPS;
- TCP where required;
- WebSocket;
- HTTP/2 where supported;
- TLS termination;
- ACME;
- custom domains;
- wildcard domains;
- hostname/path/port routing;
- load balancing;
- health-aware routing;
- weighted traffic;
- canary;
- blue/green;
- redirects;
- header policies;
- rate limits;
- connection/request limits;
- upstream logs.

External edge providers MAY be integrated.

## 29. DNS

Porter SHALL expose provider-neutral DNS operations.

Supported record types SHOULD include:

- A;
- AAAA;
- CNAME;
- TXT;
- MX;
- NS;
- SRV.

The subsystem SHOULD support:

- domain verification;
- propagation checks;
- private DNS;
- service discovery;
- ACME DNS-01;
- preview domains.

Cloudflare SHALL be a first-class integration, not a mandatory dependency.

## 30. Certificates

Certificates SHALL be first-class resources.

Lifecycle:

```text
Requested
 → Validating
 → Issuing
 → Issued
 → Deployed
 → Renewal
```

Support SHOULD include:

- ACME;
- HTTP-01;
- DNS-01;
- wildcard certificates;
- automatic renewal;
- expiry monitoring;
- deployment status.

## 31. Storage

Porter SHALL separate:

```text
ephemeral filesystem
persistent block volume
object storage
backup storage
```

Persistent volumes SHALL be independent of MicroVM lifecycle.

Volume operations SHOULD include:

- create;
- attach;
- detach;
- resize;
- snapshot;
- backup;
- restore;
- clone.

Storage classes MAY include:

- standard;
- high performance;
- NVMe;
- high IOPS;
- archive;
- shared object;
- backup.

The physical backend MAY be ext4, XFS, ZFS, LVM, raw block, NVMe or another
supported provider.

## 32. Object storage

Porter SHALL expose an `ObjectStore` abstraction.

Operations SHOULD include:

```text
Put
Get
Delete
Copy
List
MultipartUpload
Presign
Stat
SetMetadata
SetTags
ApplyLifecyclePolicy
```

Implementations MAY include:

- AWS S3;
- Cloudflare R2;
- Backblaze B2;
- Wasabi;
- Hetzner Object Storage;
- customer-managed MinIO;
- other S3-compatible providers.

MinIO MUST NOT be a mandatory hidden core dependency.

## 33. Backups and disaster recovery

Backups SHALL be independent from workload failure domains where possible.

Architecture:

```text
Workload
 ↓
Backup Controller
 ↓
Application-aware backup
 ↓
Checksum / encryption
 ↓
Object storage
 ↓
External destination
```

Database backups SHOULD use engine-aware methods.

Policies SHOULD define:

- schedule;
- retention;
- destination;
- encryption;
- verification;
- cross-node/cross-region copy;
- restore-test schedule;
- RPO;
- RTO.

A backup MUST NOT be considered fully trusted until restoration has been
successfully verified.

Restore SHOULD support:

- original resource;
- new resource;
- staging;
- different node;
- different region where supported.

## 34. Observability

Porter SHALL expose:

- logs;
- metrics;
- traces;
- events;
- health;
- alerts;
- incidents;
- deployment history;
- audit records.

Observability SHALL cover:

- control plane;
- nodes;
- agents;
- MicroVMs;
- builds;
- deployments;
- gateway;
- network;
- storage;
- billing;
- resource usage.

## 35. Events

Events SHALL be structured and versioned.

Examples:

```text
deployment.created
deployment.build.started
deployment.build.completed
deployment.failed
vm.create.started
vm.created
vm.failed
node.ready
node.unhealthy
certificate.issued
domain.attached
backup.completed
backup.failed
subscription.updated
payment.failed
incident.created
```

Operationally important events SHALL be durable.

## 36. Alerts and incidents

Alerts SHOULD support:

- thresholds;
- anomaly detection;
- absence;
- rate;
- SLO violation;
- composite conditions;
- grouping;
- deduplication;
- silencing;
- escalation;
- notification;
- automated remediation.

Incidents SHALL contain:

- severity;
- timeline;
- affected resources;
- related deployments;
- nodes/VMs;
- alerts;
- events;
- remediation;
- notes;
- resolution;
- postmortem.

## 37. SLO and synthetic monitoring

Porter SHOULD support:

- availability;
- latency;
- error rate;
- request success;
- custom SLIs;
- SLO targets;
- error budgets;
- burn-rate alerts.

Synthetic checks SHOULD cover:

- DNS;
- TLS;
- HTTP;
- TCP;
- API;
- status;
- latency;
- response validation;
- certificate validity.

## 38. Security

Security SHALL include:

- MicroVM isolation;
- tenant isolation;
- project isolation;
- RBAC;
- scoped credentials;
- service identities;
- secrets protection;
- network policy;
- TLS;
- secure sessions;
- API-key controls;
- rate limiting;
- audit logging;
- secure admin access.

Supply-chain controls SHOULD include:

- SBOM;
- provenance;
- signing;
- vulnerability scanning;
- build verification;
- policy enforcement.

Privileged operations SHALL be isolated from user-controlled workload logic.

## 39. Security center

Porter Admin SHOULD provide:

```text
Vulnerabilities
Image Scans
SBOM
Secrets
Certificates
Network Policies
Firewall
API Keys
IAM
Audit
Security Events
```

## 40. API

The Porter API SHALL be the central contract.

Resources SHOULD include:

```text
Auth
Users
Roles
Organizations
Teams
Customers
Resellers
Products
Plans
Subscriptions
Projects
Environments
Services
Deployments
Workloads
Replicas
MicroVMs
Nodes
Node Pools
Providers
Regions
Zones
Networks
Gateways
Domains
DNS
Certificates
Volumes
Object Stores
Secrets
Builds
Images
Logs
Metrics
Traces
Events
Alerts
Incidents
Snapshots
Backups
Invoices
Payments
Usage
Workflows
Audit
```

The API SHALL be versioned.

UI operations MUST correspond to API operations. The UI must not contain hidden
business logic or privileged actions unavailable to the API.

## 41. Streaming

The API SHOULD provide event/operation streaming through WebSocket or an
equivalent mechanism.

Long-running operations MUST expose a task/operation ID.

## 42. CLI

The CLI SHOULD consume the same API.

Example commands:

```text
porter login
porter project create
porter deploy
porter deployment list
porter logs
porter domain add
porter vm list
porter node list
porter scale
porter rollback
porter doctor
```

## 43. Infrastructure automation

Porter SHOULD support:

- Terraform provider;
- GitHub Actions;
- webhooks;
- declarative configuration;
- SDKs.

Infrastructure automation MUST use the same policy, RBAC, audit and idempotency
model as the UI.

## 44. AI operations

AI agents SHALL be first-class identities.

Agent model:

```text
Agent
 ├── owner
 ├── organization
 ├── roles
 ├── permissions
 ├── resource scope
 ├── environment scope
 ├── tool permissions
 ├── approval policy
 ├── rate limits
 └── audit configuration
```

AI SHALL use typed tools such as:

```text
inspect_resource
list_resources
get_logs
query_metrics
query_traces
get_events
get_topology
get_usage
get_cost
create_deployment
scale_service
rollback_deployment
restart_workload
create_snapshot
restore_backup
modify_network
manage_domain
manage_certificate
run_diagnostics
```

Arbitrary shell access MUST be an explicit privileged capability.

Non-trivial AI changes SHOULD create a plan containing:

```text
intent
current state
proposed state
resource changes
dependencies
risk
cost impact
permissions
approval
execution steps
verification
rollback
```

## 45. Extensions and marketplace

Third-party extensions MUST NOT receive unrestricted PostgreSQL access.

Extension identity SHALL include:

- publisher;
- version;
- permissions;
- secrets required;
- API capabilities;
- events;
- UI contributions;
- health;
- compatibility.

Third-party extension code SHOULD run out-of-process or remotely.

Marketplace content MAY include:

- provider adapters;
- payment adapters;
- DNS adapters;
- accounting;
- identity;
- communications;
- monitoring;
- application templates;
- workflow packs;
- AI skills and agents.

Core Porter functionality MUST remain useful without marketplace installation.

## 46. Commercial platform

Porter SHALL model separately:

```text
Product
Plan
Price / Charge
Entitlement
Subscription
Service
Usage Event
Meter
Invoice
Payment
Credit
Refund
Tax
```

Entitlements SHALL influence runtime admission and quota enforcement.

Example entitlements:

```text
max_vcpu
max_memory
max_storage
max_domains
max_projects
max_public_ips
max_volumes
private_networking
custom_domains
gpu
```

## 47. Metering

Usage events SHALL include:

```text
event_id
customer_id
organization_id
subscription_id
resource_id
metric_code
quantity
timestamp
dimensions
idempotency_key
```

Initial meters SHOULD include:

- vCPU time;
- memory time;
- storage GB-month;
- object storage GB-month;
- network bytes;
- public IP time;
- snapshots;
- backups;
- build minutes;
- deployments;
- function invocations;
- queue messages;
- log volume;
- trace volume.

Metering MUST be replayable and deduplicated.

## 48. Subscription lifecycle

```text
DRAFT
 ↓
PENDING
 ↓
TRIALING
 ↓
ACTIVE
 ↓
PAST_DUE
 ↓
RECOVERY
 ↓
SUSPENDED
 ↓
CANCELLED
 ↓
EXPIRED
```

Billing state MUST be separate from runtime state.

Payment failure SHOULD invoke a workflow such as:

```text
Payment failed
 ↓
Retry
 ↓
Notify
 ↓
Grace period
 ↓
Retry / alternate method
 ↓
Past due
 ↓
Policy
 ↓
Suspend if required
```

## 49. Billing traceability

Every invoice line SHOULD be traceable:

```text
Invoice
 ↓
Invoice Line
 ↓
Charge
 ↓
Meter
 ↓
Usage Events
 ↓
Resource
 ↓
Node / Region / Provider
```

An operator MUST be able to determine why a customer was charged.

## 50. Admin UI

Porter Admin SHALL provide one provider/operator console.

Major areas:

```text
Overview
Customers
Organizations
Users
Resellers
Services
Subscriptions
Domains

Compute
 Applications
 VMs
 Workers
 Functions
 Jobs
 Cron
 Builds
 Images
 Snapshots

Infrastructure
 Providers
 Regions
 Zones
 Nodes
 Node Pools
 Capacity
 Scheduler
 Firecracker
 Build Workers

Network
 Networks
 IPs
 Gateways
 Load Balancers
 DNS
 Certificates
 Firewall
 Security Groups

Storage
 Volumes
 Snapshots
 Backups
 Storage Classes
 Object Storage

Observability
 Dashboard
 Metrics
 Logs
 Traces
 Events
 Alerts
 SLOs
 Synthetic Checks
 Incidents
 Status

Security
 Vulnerabilities
 SBOM
 Secrets
 API Keys
 Policies
 Audit

Billing
 Products
 Plans
 Prices
 Entitlements
 Subscriptions
 Usage
 Invoices
 Payments
 Credits
 Coupons
 Taxes
 Dunning
 Revenue

Automation
 Workflows
 Webhooks
 Schedules
 Tasks
 Queues
 Notifications

Platform
 Settings
 Integrations
 API
 CLI
 System Health
 Maintenance
```

Navigation SHALL be RBAC-aware.

## 51. Customer control panel

Customer UI SHOULD focus on:

```text
Dashboard
Projects
Applications
Deployments
Environments
Domains
Variables
Secrets
Storage
Logs
Metrics
Networking
Cron
Backups
Team
Usage
Billing
Settings
```

Normal users MUST NOT need to understand Firecracker, KVM, Linux networking,
scheduler internals or VM lifecycle.

## 52. UX requirements

Porter SHALL follow:

```text
Intent
 ↓
Guided control
 ↓
Advanced infrastructure
```

Common operations SHOULD require approximately three meaningful interactions.

The UI SHALL:

- expose one primary action;
- use explicit operational states;
- avoid unnecessary navigation;
- preserve context;
- provide global search;
- provide command palette;
- provide resource timelines;
- show precise values and timestamps;
- support dense tables;
- support keyboard navigation;
- distinguish UI latency from infrastructure latency.

Automatic decisions SHALL follow:

```text
Automatic
 ↓
Explain
 ↓
Override
```

## 53. Resource pages

Every major resource page SHOULD contain:

```text
Header
Summary
Activity
Configuration
Observability
Dependencies
Relationships
Infrastructure
Audit
Danger Zone
```

Customer 360 SHALL combine commercial and infrastructure context.

Infrastructure 360 SHALL allow:

```text
Provider
 → Region
 → Zone
 → Node Pool
 → Node
 → MicroVM
 → Workload
 → Deployment / Network / Storage / Domain / Telemetry
```

## 54. Tasks and workflows

Long-running work SHALL be represented as durable operations/tasks.

States:

```text
Queued
Running
Waiting
Succeeded
Failed
Retrying
Cancelled
PartiallySucceeded
NeedsAttention
```

Examples:

- build;
- deploy;
- VM create;
- migrate;
- snapshot;
- backup;
- restore;
- DNS update;
- certificate issue;
- invoice generation.

Workflows SHALL be composed from:

```text
Trigger
Condition
Action
Schedule
```

## 55. Operational safety

Destructive operations SHOULD provide:

- confirmation;
- RBAC;
- audit;
- dependency checks;
- impact preview;
- backup/snapshot option;
- rollback where possible;
- maintenance windows.

Bulk operations MUST use the same policy, authorization, rate-limit and audit
systems as single-resource operations.

## 56. Impersonation

If support impersonation exists, it SHALL be:

```text
Request
 ↓
Policy check
 ↓
Optional customer consent
 ↓
Time-limited session
 ↓
Visible banner
 ↓
Full audit
 ↓
Automatic expiry
```

Silent unrestricted impersonation is prohibited.

## 57. Performance

The UI SHOULD use:

- incremental loading;
- server-side filtering;
- server-side sorting;
- pagination;
- virtualization;
- cached navigation state;
- streaming operation updates;
- WebSocket events where appropriate.

Infrastructure operations may be slow; the interface must remain responsive.

## 58. Availability and disruption

Porter SHOULD provide a Workload Disruption Budget concept.

Example:

```text
10 replicas
minimum available = 8
maintenance may evict at most 2
```

This MUST integrate with node draining, upgrades and rescheduling.

## 59. Maintenance

Maintenance operations SHALL support:

- node cordon;
- node drain;
- workload replacement;
- workload migration where supported;
- maintenance windows;
- notifications;
- health verification;
- rollback/recovery.

## 60. Data model minimum

The database SHALL represent at least:

```text
installations
providers
regions
zones
node_pools
nodes
users
roles
permissions
customers
resellers
organizations
teams
projects
environments
services
workloads
deployments
replicas
microvms
networks
ip_allocations
gateways
routes
domains
dns_zones
dns_records
certificates
volumes
snapshots
backups
object_stores
secrets
builds
artifacts
images
events
tasks
workflows
alerts
incidents
audit_events
products
plans
prices
entitlements
subscriptions
usage_events
meters
charges
invoices
payments
credits
refunds
tickets
extensions
agents
```

Schema details belong in migrations and subsystem specifications.

## 61. State ownership

The implementation SHALL preserve:

```text
PostgreSQL → durable control-plane truth
Redis      → optional acceleration
Porter     → desired state / orchestration
Agent      → host execution bridge
Firecracker→ VM runtime
Linux      → kernel primitives
BuildKit   → build engine
```

No component may silently become the authoritative owner of another
component's state.

## 62. Testing requirements

Every subsystem SHALL have:

- unit tests;
- integration tests;
- failure-path tests;
- idempotency tests;
- authorization tests.

The runtime/networking/scheduler layers SHOULD have Linux integration tests on
supported host configurations.

Critical end-to-end tests SHALL cover:

1. node enrollment;
2. KVM/Firecracker preflight;
3. VM creation;
4. VM restart/recovery;
5. network attachment;
6. Git build;
7. artifact preparation;
8. deployment;
9. health check;
10. gateway activation;
11. domain/TLS;
12. rollback;
13. node failure;
14. reconciliation after controller restart;
15. quota enforcement;
16. secret isolation;
17. audit generation;
18. backup/restore.

## 63. Acceptance criteria

### A. Runtime

A valid node can create, start, inspect, stop, restart, recover and delete a
MicroVM through the Porter API.

### B. Deployment

A connected Git repository can be built and deployed into a MicroVM with visible
operation states.

### C. Networking

A healthy application can receive traffic through the Porter gateway.

### D. Domain

A configured DNS provider can be used to verify a domain and activate TLS.

### E. Recovery

If a controller crashes during an operation, the next reconciliation cycle
converges to the correct desired state.

### F. Multi-node

A workload can be placed on an eligible node according to capacity and policy.

### G. Security

A workload/customer cannot access another tenant's secrets, resources or
authorized control-plane operations.

### H. Commercial

A provisioned service can be associated with a subscription and emit usage
events traceable to billing.

### I. Audit

Security-sensitive and administrative operations produce durable audit events.

### J. AI

An AI agent can inspect and perform only operations allowed by its identity and
policy.

## 64. First vertical slice

The first complete production-oriented vertical slice SHALL be:

```text
GitHub
 ↓
Build
 ↓
OCI artifact
 ↓
Guest/rootfs preparation
 ↓
Firecracker
 ↓
Network
 ↓
Gateway
 ↓
Domain
 ↓
TLS
 ↓
Healthy application
```

This path takes precedence over building isolated dashboard features.

## 65. Release phases

### v0.1.0-beta

Runtime foundation:

- Go control plane;
- PostgreSQL;
- node enrollment;
- Firecracker;
- KVM preflight;
- basic networking;
- VM lifecycle;
- desired/actual state;
- basic API;
- basic UI;
- logs/health.

### v0.2.0

Build and artifact foundation:

- BuildKit;
- OCI artifacts;
- build queue;
- artifact storage;
- deployment records;
- deployment logs.

### v0.3.0

Public application path:

- gateway;
- domains;
- DNS;
- TLS;
- routing;
- health-gated activation.

### v0.4.0

Multi-node:

- node pools;
- scheduler;
- capacity;
- replicas;
- drain;
- rescheduling.

### v0.5.0

Platform operations:

- environments;
- secrets;
- volumes;
- rollback;
- deployment strategies;
- backups.

### v0.6.0

Observability:

- metrics;
- logs;
- traces;
- events;
- alerts;
- incidents;
- topology;
- Doctor.

### v0.7.0

Commercial control plane:

- customers;
- products;
- plans;
- entitlements;
- subscriptions;
- usage;
- invoices;
- payments;
- quotas;
- resellers.

### v0.8.0

Native services and integrations:

- object storage abstraction;
- databases;
- queue;
- cache;
- email;
- provider adapters;
- Cloudflare integration.

### v0.9.0

Advanced orchestration:

- autoscaling;
- disruption budgets;
- migration;
- snapshots;
- advanced placement;
- disaster recovery.

### v0.10.0

Extension and marketplace foundation:

- extension SDK;
- signed manifests;
- permission model;
- marketplace;
- workflow packs.

### v1.0.0-alpha

Hardening:

- security review;
- HA control-plane preparation;
- upgrade/recovery;
- compatibility guarantees;
- performance testing;
- full acceptance suite.

### v1.1.0-final

Production release target:

- documented deployment;
- stable API;
- upgrade path;
- backup/restore verification;
- operational tooling;
- security posture;
- complete core golden paths.

## 66. Implementation-agent rules

An implementation agent MUST:

1. Read this SRS before changing architecture.
2. Read `ARCHITECTURE_FLOW.md` before implementing a subsystem.
3. Treat PostgreSQL as durable truth.
4. Preserve desired/actual state separation.
5. Make infrastructure actions idempotent.
6. Put authorization at the API/control-plane boundary.
7. Keep privileged host actions behind the agent/runtime boundary.
8. Avoid adding Kubernetes or Docker as hidden core dependencies.
9. Avoid daemon proliferation without an explicit reason.
10. Avoid putting business logic in the frontend.
11. Never fake unimplemented functionality.
12. Add tests for every new controller and failure path.
13. Record implementation status as implemented, partial, experimental or planned.
14. Preserve the resource graph and public API semantics.
15. Prefer deterministic workflows over AI for deterministic operations.

## 67. Core invariants

These invariants MUST remain true:

1. Firecracker is the workload isolation boundary.
2. PostgreSQL is authoritative durable state.
3. Redis is optional acceleration.
4. OCI images are artifacts, not inherently VMs.
5. BuildKit builds; it does not run customer workloads.
6. Desired state drives reconciliation.
7. Controllers are idempotent.
8. Important actions are observable and auditable.
9. AI cannot bypass authorization.
10. Extensions cannot bypass authorization.
11. Customer workloads are isolated from control-plane state.
12. Cloudflare is optional.
13. MinIO is optional and provider-neutral object storage is supported.
14. Billing state is separate from runtime state.
15. UI, CLI, automation and AI use the same API.
16. Complex capabilities always have a simple default path and an advanced escape hatch.
