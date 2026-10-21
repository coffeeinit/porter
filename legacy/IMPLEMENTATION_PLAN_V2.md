# Porter Implementation Plan v2.0 - Focused, MVP-First

**Based on**: SRS v2.0  
**Approach**: MVP → Production → Platform → Commercial  
**Timeline**: 4-6 weeks to MVP, 8-12 weeks to Production

---

## Overview

This plan focuses on **delivering value first**. We start with a working MVP (deploy from Git to Firecracker with AI support), then add layers as needed.

**Key Changes from v1 Plan:**
- ✅ Prioritized layers (MVP first)
- ✅ AI integrated from day 1 (not afterthought)
- ✅ Each layer is releasable
- ✅ Can stop at any layer
- ✅ Clear success criteria per layer

---

## Layer 1: MVP (Core + AI) - 4-6 weeks

### Goal
Deploy from Git to Firecracker MicroVM with AI-first design.

### Week 1-2: Repository Cleanup & Core Runtime

**Tasks:**
1. **Repository Restructure**
   - Archive `legacy/` to branch
   - Delete `data/`, `.gocache/`
   - Move `porter-images/` → `images/`
   - Move `backend/internal` → `internal/`
   - Move `backend/cmd` → `cmd/`
   - Create `internal/sdk/`
   - Update all imports

2. **PostgreSQL Schema**
   ```sql
   CREATE TABLE projects (
       id UUID PRIMARY KEY,
       name VARCHAR(255) NOT NULL,
       created_at TIMESTAMP DEFAULT NOW()
   );

   CREATE TABLE deployments (
       id UUID PRIMARY KEY,
       project_id UUID REFERENCES projects(id),
       source_type VARCHAR(50),
       source_ref VARCHAR(255),
       artifact_digest VARCHAR(255),
       status VARCHAR(50),
       created_at TIMESTAMP DEFAULT NOW()
   );

   CREATE TABLE vms (
       id UUID PRIMARY KEY,
       deployment_id UUID REFERENCES deployments(id),
       state VARCHAR(50),
       cpu_cores INT,
       memory_mb INT,
       disk_gb INT,
       created_at TIMESTAMP DEFAULT NOW()
   );

   CREATE TABLE audit_log (
       id UUID PRIMARY KEY,
       actor_type VARCHAR(50),
       actor_id VARCHAR(255),
       action VARCHAR(255),
       resource VARCHAR(255),
       outcome VARCHAR(20),
       timestamp TIMESTAMP DEFAULT NOW()
   );
   ```

3. **Firecracker Driver**
   - VM lifecycle: create, start, stop, delete
   - Kernel boot integration
   - Config drive for guest init
   - Mock driver (file-based for dev)

4. **Basic API**
   ```go
   // System
   GET  /health
   GET  /version
   GET  /api/discover

   // Auth
   POST /auth/login
   POST /auth/token

   // Projects
   GET    /api/projects
   POST   /api/projects
   GET    /api/projects/{id}
   DELETE /api/projects/{id}

   // Deployments
   POST   /api/projects/{id}/deployments
   GET    /api/projects/{id}/deployments
   GET    /api/deployments/{id}

   // VMs
   GET    /api/vms
   GET    /api/vms/{id}
   POST   /api/vms/{id}/start
   POST   /api/vms/{id}/stop
   DELETE /api/vms/{id}

   // Logs
   GET    /api/vms/{id}/logs
   GET    /api/vms/{id}/logs/stream
   ```

5. **Reconciliation Engine**
   - 2s tick loop
   - Desired → actual state
   - Idempotent operations

**Deliverables:**
- ✅ Repository restructured
- ✅ PostgreSQL schema
- ✅ Firecracker driver (mock + real)
- ✅ Basic API working
- ✅ Can create projects and VMs

### Week 3-4: Build System

**Tasks:**
1. **Git Integration**
   - GitHub webhook handler
   - Git clone
   - Branch/commit detection
   - Build plan generation

2. **BuildKit Integration**
   - `buildctl` driver
   - Dockerfile builds
   - Build context management
   - Log streaming
   - Build caching

3. **OCI Artifacts**
   - Image pull (go-containerregistry)
   - Layer unpacking with whiteouts
   - Artifact storage
   - Digest-based caching

4. **Image Catalog**
   - Seed built-in images (nginx, postgres, redis)
   - Image metadata (ports, volumes, health)
   - Catalog API

**Deliverables:**
- ✅ Deploy from Git repository
- ✅ Deploy from OCI registry
- ✅ BuildKit builds with logs
- ✅ Image catalog with common apps

### Week 5-6: AI Integration

**Tasks:**
1. **AI Role in Auth**
   ```go
   const RoleAI = "ai"

   var rolePermissions = map[string][]string{
       RoleAI: {
           "project.read", "project.create",
           "deployment.create", "deployment.read",
           "vm.start", "vm.stop", "vm.restart",
           // Not: project.delete (requires approval)
       },
   }
   ```

2. **/api/discover Endpoint**
   ```go
   GET /api/discover
   Response:
   {
     "version": "v1.0.0",
     "routes": [
       {
         "method": "POST",
         "path": "/api/projects/{id}/deployments",
         "description": "Deploy from Git or OCI",
         "auth_required": true,
         "ai_permitted": true,
         "parameters": {...},
         "response": {...}
       }
     ],
     "resources": {...}
   }
   ```

3. **SDK (internal/sdk/)**
   ```go
   package sdk

   type Client struct {
       baseURL    string
       authToken  string
   }

   func (c *Client) Discover(ctx) (*APIDiscovery, error)
   func (c *Client) DeployFromGit(ctx, repo, branch string) (*Deployment, error)
   func (c *Client) ScaleService(ctx, serviceID string, replicas int) error
   func (c *Client) GetLogs(ctx, vmID string) (string, error)
   ```

4. **AI Audit Trail**
   - Log all AI actions
   - Actor type tracking
   - Approval gates
   - Audit query API

**Deliverables:**
- ✅ AI role functional
- ✅ /api/discover returns full API surface
- ✅ SDK can be imported and used
- ✅ AI actions audited

### MVP Success Criteria
- ✅ Deploy from Git to Firecracker
- ✅ AI can call /api/discover and deploy
- ✅ Logs streaming works
- ✅ Single-node reliable
- ✅ Mock driver for dev

---

## Layer 2: Production Ready - 4-6 weeks

### Goal
Add networking, storage, observability, and multi-node support.

### Week 1-2: Networking & Gateway

**Tasks:**
1. **Caddy Integration**
   - HTTP/HTTPS routing
   - Custom domain support
   - TLS via ACME (Let's Encrypt)
   - Port proxy (host → VM)

2. **IPAM**
   - /30 per VM
   - TAP devices
   - NAT for outbound
   - VM-to-VM communication

3. **Domains API**
   ```go
   POST   /api/projects/{id}/domains
   GET    /api/projects/{id}/domains
   DELETE /api/domains/{id}
   POST   /api/domains/{id}/verify
   ```

**Deliverables:**
- ✅ Custom domains with HTTPS
- ✅ Caddy reverse proxy
- ✅ Networking primitives working

### Week 3-4: Storage & Health

**Tasks:**
1. **Volumes**
   - ext4 volume images
   - Create/attach/detach
   - Resize
   - Snapshot

2. **Health Checks**
   - TCP/HTTP probes
   - Configurable intervals
   - Unhealthy streak detection

3. **Restart Policies**
   - never
   - on-failure
   - always

**Deliverables:**
- ✅ Persistent volumes
- ✅ Health checks
- ✅ Restart policies

### Week 5-6: Observability & Multi-Node

**Tasks:**
1. **Observability**
   - Per-VM console logs
   - Log aggregation
   - Log search
   - SSE streaming
   - Metrics (CPU, memory, disk, network)
   - Event log

2. **Scheduler**
   - Bin packing
   - Resource scoring
   - Placement decisions
   - Affinity/anti-affinity

3. **Node Management**
   - Node enrollment
   - Health checks
   - Cordon/drain
   - Maintenance mode

**Deliverables:**
- ✅ Observability complete
- ✅ Multi-node scheduling
- ✅ Node management

### Production Success Criteria
- ✅ Custom domains with HTTPS
- ✅ Persistent volumes
- ✅ Multi-node scheduling
- ✅ Observability complete
- ✅ 99.9% uptime

---

## Layer 3: Platform Features - 4-6 weeks

### Goal
Add environments, secrets, RBAC, and advanced features.

### Week 1-2: Environments & Secrets

**Tasks:**
1. **Environments**
   ```go
   project/
     ├── dev/
     ├── staging/
     └── production/
   ```

2. **Secrets**
   - Encrypted storage
   - Scoped injection
   - Rotation
   - Audit logging

3. **Variables**
   - Environment variables
   - Per-environment vars
   - Secret flag (masked in UI)

**Deliverables:**
- ✅ Environment management
- ✅ Secret storage
- ✅ Variable injection

### Week 3-4: RBAC & Teams

**Tasks:**
1. **Roles**
   - admin, developer, viewer, ai
   - Role-based permissions
   - Custom roles

2. **Teams**
   - Team management
   - Team members
   - Team projects

3. **Permissions**
   - project.read, project.create, project.delete
   - deployment.create, deployment.rollback
   - vm.start, vm.stop, vm.delete

**Deliverables:**
- ✅ RBAC working
- ✅ Team management
- ✅ Permissions enforced

### Week 5-6: Advanced Features

**Tasks:**
1. **Preview Environments**
   - Auto-create from PRs
   - Auto-delete after merge
   - Unique domains

2. **Rolling Deployments**
   - Zero-downtime updates
   - Canary deployments
   - Rollback support

3. **Autoscaling**
   - CPU-based scaling
   - Memory-based scaling
   - Custom metrics

**Deliverables:**
- ✅ Preview environments
- ✅ Rolling deployments
- ✅ Autoscaling

### Platform Success Criteria
- ✅ Environments (dev/staging/prod)
- ✅ Secrets management
- ✅ RBAC working
- ✅ Teams and organizations

---

## Layer 4: Commercial - 4-6 weeks

### Goal
Add billing, subscriptions, and marketplace.

### Week 1-2: Billing Foundation

**Tasks:**
1. **Products & Plans**
   ```go
   type Product struct {
       ID string
       Name string
       Plans []Plan
   }

   type Plan struct {
       ID string
       Name string
       Price float64
       Entitlements []Entitlement
   }
   ```

2. **Usage Metering**
   ```go
   type UsageEvent struct {
       ID string
       CustomerID string
       MetricCode string
       Quantity float64
       Timestamp time.Time
   }
   ```

3. **Invoicing**
   - Invoice generation
   - Invoice line items
   - Tax calculation

**Deliverables:**
- ✅ Products & plans
- ✅ Usage metering
- ✅ Invoicing

### Week 3-4: Subscriptions

**Tasks:**
1. **Subscription Lifecycle**
   ```go
   type Subscription struct {
       ID string
       CustomerID string
       PlanID string
       Status string  // 'active', 'past_due', 'cancelled'
       BillingPeriod string
   }
   ```

2. **Payment Processing**
   - Stripe integration
   - Payment methods
   - Webhooks

3. **Dunning**
   - Retry logic
   - Grace periods
   - Suspension

**Deliverables:**
- ✅ Subscription lifecycle
- ✅ Payment processing
- ✅ Dunning management

### Week 5-6: Marketplace

**Tasks:**
1. **Extension System**
   - Extension SDK
   - Signed manifests
   - Permission model

2. **Marketplace UI**
   - Extension catalog
   - Installation flow
   - Rating system

3. **Third-party Integrations**
   - DNS providers
   - Payment adapters
   - Email services

**Deliverables:**
- ✅ Extension system
- ✅ Marketplace UI
- ✅ Third-party integrations

### Commercial Success Criteria
- ✅ Billing and subscriptions
- ✅ Usage metering
- ✅ Payment processing
- ✅ Reseller support

---

## Repository Structure (Final)

```
porter/
├── cmd/
│   ├── porter/              # Main binary
│   │   └── main.go
│   └── porterctl/           # CLI
│       └── main.go
├── internal/
│   ├── api/                 # REST API
│   │   ├── handlers/
│   │   │   ├── discover.go  # /api/discover
│   │   │   ├── projects.go
│   │   │   ├── deployments.go
│   │   │   ├── vms.go
│   │   │   ├── domains.go
│   │   │   ├── volumes.go
│   │   │   └── secrets.go
│   │   ├── middleware/
│   │   │   ├── auth.go      # AI role handling
│   │   │   ├── rbac.go
│   │   │   └── audit.go
│   │   └── routes.go
│   ├── sdk/                 # AI SDK
│   │   ├── client.go
│   │   ├── types.go
│   │   └── discover.go
│   ├── control/             # Reconciliation
│   │   ├── engine.go
│   │   ├── scheduler.go
│   │   └── controller.go
│   ├── store/               # PostgreSQL
│   │   ├── migrations/
│   │   ├── models.go
│   │   └── queries.go
│   ├── runtime/             # Firecracker
│   │   ├── firecracker/
│   │   ├── mock/
│   │   └── driver.go
│   ├── build/               # BuildKit
│   │   └── buildkit.go
│   ├── image/               # OCI artifacts
│   │   └── oci.go
│   ├── networking/          # IPAM, TAP
│   │   ├── ipam.go
│   │   └── tap.go
│   ├── storage/             # Volumes
│   │   └── volume.go
│   └── bootstrap/           # Guest init
│       └── init.go
├── images/                   # Image catalog
│   ├── catalog.json
│   └── bases/
├── test/
│   ├── e2e/
│   └── ai-test/
├── scripts/
├── docs/
├── go.mod
├── README.md
└── LICENSE
```

---

## Timeline Summary

| Layer | Duration | Focus | Success |
|-------|----------|-------|---------|
| Layer 1: MVP | 4-6 weeks | Core + AI | Deploy from Git, AI can deploy |
| Layer 2: Production | 4-6 weeks | Networking, Storage, Multi-node | Domains, Volumes, Multi-node |
| Layer 3: Platform | 4-6 weeks | Environments, RBAC, Scaling | Teams, Secrets, Autoscaling |
| Layer 4: Commercial | 4-6 weeks | Billing, Marketplace | Subscriptions, Payments |

**Total to Complete**: 16-24 weeks (4-6 months)

**Stop at Any Layer**: Each layer is a complete, releasable product.

---

## Decision Points

### After MVP (Layer 1)
- Is MVP sufficient for your use case?
- Do you need public access (domains)?
- Do you need multi-node?

### After Production (Layer 2)
- Do you need teams and RBAC?
- Do you need environments?
- Do you need autoscaling?

### After Platform (Layer 3)
- Do you need billing?
- Do you need resellers?
- Do you need marketplace?

---

## Next Steps

1. **Review SRS v2.0 and Implementation Plan v2.0**
2. **Approve or adjust**
3. **Start Layer 1, Week 1** (Repository cleanup & core runtime)
4. **Deliver MVP** in 4-6 weeks
5. **Decide on next layer**

---

## Philosophy

**"Deliver value first, add complexity when needed."**

- **MVP**: Deploy from Git to Firecracker with AI
- **Production**: Add networking, storage, multi-node
- **Platform**: Add teams, environments, scaling
- **Commercial**: Add billing, marketplace

**Each layer is a complete product.**
