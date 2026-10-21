# Porter SRS v2.0 - Focused, Flow-Based Specification

**Version**: 2.0  
**Status**: Prioritized, implementation-ready  
**Philosophy**: Start simple, add complexity when needed

---

## Core Thesis

**Porter is an AI-first MicroVM platform that deploys applications from Git with Vercel-like simplicity.**

Key differentiators:
1. **AI as a first-class citizen** - not an afterthought
2. **Firecracker isolation** - containers are inputs, not runtime
3. **Self-hostable** - runs on one server, scales to many
4. **Pure OSS** - AGPL 3.0, no vendor lock-in
5. **Composable** - use what you need, ignore the rest

---

## Priority Layers (MVP → Production)

### Layer 1: Core MVP (Essential for first release)
- ✅ Deploy from Git to Firecracker
- ✅ OCI image support
- ✅ Basic networking (VM can access internet)
- ✅ Logs streaming
- ✅ AI role with /api/discover
- ✅ SDK for AI consumption
- ✅ Single-node only

### Layer 2: Production Ready (After MVP)
- ✅ Custom domains with HTTPS
- ✅ Persistent volumes
- ✅ Health checks & restart policies
- ✅ Environment variables & secrets
- ✅ Multi-node scheduling
- ✅ Observability (logs, metrics, events)

### Layer 3: Platform Features (Later)
- ✅ Preview environments
- ✅ Rolling deployments
- ✅ Autoscaling
- ✅ Backup & restore
- ✅ RBAC & teams
- ✅ CLI & Terraform provider

### Layer 4: Commercial (Optional)
- ✅ Billing & subscriptions
- ✅ Usage metering
- ✅ Reseller support
- ✅ Marketplace

---

## Simplified Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Porter Control Plane                │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │   API + SDK  │  │ Reconciler   │  │   AI Role    │ │
│  └──────────────┘  └──────────────┘  └──────────────┘ │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │  PostgreSQL  │  │  BuildKit    │  │   Gateway    │ │
│  └──────────────┘  └──────────────┘  └──────────────┘ │
└─────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│                   Firecracker MicroVMs                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐│
│  │   App 1  │  │   App 2  │  │   App 3  │  │   App 4  ││
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘│
└─────────────────────────────────────────────────────────┘
```

**Key Principle**: Each layer is optional. You can run:
- API + PostgreSQL + Firecracker (minimal)
- + BuildKit (Git deployments)
- + Gateway (public access)
- + AI role (automation)

---

## AI-First Design

### AI as a Role (Not Separate System)

```go
// AI authenticates like any user
POST /auth/login
{
  "username": "ai-agent-001",
  "password": "api-key-xyz"
}

// Response includes role
{
  "token": "jwt...",
  "role": "ai"
}
```

### /api/discover Endpoint

AI calls this to learn the API:

```http
GET /api/discover
```

Response:
```json
{
  "version": "v1.0.0",
  "routes": [
    {
      "method": "POST",
      "path": "/projects/{id}/deployments",
      "description": "Deploy from Git or OCI",
      "auth_required": true,
      "ai_permitted": true,
      "parameters": {
        "source": "git",
        "repository": "string",
        "branch": "string"
      },
      "response": {
        "deployment_id": "string",
        "status": "building"
      }
    }
    // ... all routes
  ],
  "resources": {
    "projects": "/api/projects",
    "deployments": "/api/projects/{id}/deployments",
    "vms": "/api/vms"
  }
}
```

### SDK (internal/sdk/)

```go
package sdk

type Client struct {
    baseURL    string
    authToken  string
}

// High-level AI operations
func (c *Client) DeployFromGit(ctx, repo, branch string) (*Deployment, error)
func (c *Client) ScaleService(ctx, serviceID string, replicas int) error
func (c *Client) GetLogs(ctx, vmID string) (string, error)
func (c *Client) Diagnose(ctx, vmID string) (*Diagnosis, error)
```

### AI Audit Trail

Every AI action is logged:

```sql
CREATE TABLE audit_log (
    id UUID PRIMARY KEY,
    actor_type VARCHAR(50),  -- 'user', 'ai', 'service-account'
    actor_id VARCHAR(255),
    action VARCHAR(255),
    resource VARCHAR(255),
    outcome VARCHAR(20),     -- 'allowed', 'denied', 'failed'
    timestamp TIMESTAMP DEFAULT NOW()
);
```

---

## Layer 1: Core MVP Specification

### 1.1 Deployment Flow

```
User/AI Agent
    ↓
POST /api/projects/{id}/deployments
{
  "source": "git",
  "repository": "github.com/user/app",
  "branch": "main"
}
    ↓
Porter clones repo
    ↓
BuildKit builds OCI artifact
    ↓
Porter converts OCI → Firecracker rootfs
    ↓
Firecracker boots MicroVM
    ↓
Health check
    ↓
Ready (logs streamed via SSE)
```

### 1.2 Core API Routes

```go
// System
GET  /health
GET  /version
GET  /api/discover          // AI learns API

// Auth
POST /auth/login
POST /auth/token            // for AI agents

// Projects
GET    /api/projects
POST   /api/projects
GET    /api/projects/{id}
PATCH  /api/projects/{id}
DELETE /api/projects/{id}

// Deployments
POST   /api/projects/{id}/deployments
GET    /api/projects/{id}/deployments
GET    /api/deployments/{id}
POST   /api/deployments/{id}/rollback

// VMs
GET    /api/vms
GET    /api/vms/{id}
POST   /api/vms/{id}/start
POST   /api/vms/{id}/stop
POST   /api/vms/{id}/restart
DELETE /api/vms/{id}

// Logs
GET    /api/vms/{id}/logs
GET    /api/vms/{id}/logs/stream  // SSE
```

### 1.3 Data Model (Minimal)

```sql
CREATE TABLE projects (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE TABLE deployments (
    id UUID PRIMARY KEY,
    project_id UUID REFERENCES projects(id),
    source_type VARCHAR(50),  -- 'git', 'oci'
    source_ref VARCHAR(255),
    artifact_digest VARCHAR(255),
    status VARCHAR(50),       -- 'building', 'deploying', 'running', 'failed'
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE TABLE vms (
    id UUID PRIMARY KEY,
    deployment_id UUID REFERENCES deployments(id),
    state VARCHAR(50),        -- 'pending', 'running', 'stopped', 'failed'
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

### 1.4 Runtime Requirements

**Single-Node MVP:**
- Linux with KVM
- Firecracker binary
- PostgreSQL
- BuildKit (optional, for Git deployments)
- Go 1.22+

**No Required for MVP:**
- ❌ Multi-node scheduling
- ❌ Gateway/Caddy
- ❌ Custom domains
- ❌ TLS
- ❌ Volumes
- ❌ Secrets
- ❌ RBAC
- ❌ Billing

---

## Layer 2: Production Ready (After MVP)

### 2.1 Networking

**Gateway Integration:**
- Caddy for HTTP/HTTPS
- Custom domain support
- TLS via ACME (Let's Encrypt)
- Port proxy (host → VM)

**IPAM:**
- /30 per VM
- TAP devices
- NAT for outbound
- VM-to-VM communication

### 2.2 Storage

**Volumes:**
- ext4 volume images
- Create/attach/detach
- Resize
- Snapshot

### 2.3 Observability

**Logs:**
- Per-VM console logs
- Log aggregation
- Log search
- SSE streaming

**Metrics:**
- CPU, memory, disk, network
- Application metrics
- Prometheus integration

**Events:**
- Event log
- Event streaming
- Event filtering

### 2.4 Health & Reliability

**Health Checks:**
- TCP/HTTP probes
- Configurable intervals
- Unhealthy streak detection

**Restart Policies:**
- never
- on-failure
- always

**Reconciliation:**
- 2s tick loop
- Desired → actual state
- Idempotent operations

---

## Layer 3: Platform Features (Later)

### 3.1 Environments

```
project/
  ├── dev/
  ├── staging/
  └── production/
```

Each environment has:
- Variables
- Secrets
- Resource limits
- Deployment strategy

### 3.2 Secrets

```go
POST /api/projects/{id}/secrets
{
  "name": "DATABASE_URL",
  "value": "postgres://...",
  "environment": "production"
}
```

Encrypted at rest, injected into VM at boot.

### 3.3 RBAC

```go
// Roles
admin, developer, viewer, ai

// Permissions
project.read, project.create, project.delete
deployment.create, deployment.rollback
vm.start, vm.stop, vm.delete
```

### 3.4 Multi-Node

**Scheduler:**
- Bin packing
- Resource scoring
- Placement decisions
- Affinity/anti-affinity

**Node Management:**
- Node enrollment
- Health checks
- Cordon/drain
- Maintenance mode

---

## Layer 4: Commercial (Optional)

### 4.1 Billing

**Products & Plans:**
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
    Currency string
    Entitlements []Entitlement
}
```

**Usage Metering:**
```go
type UsageEvent struct {
    ID string
    CustomerID string
    MetricCode string
    Quantity float64
    Timestamp time.Time
}
```

### 4.2 Subscriptions

```go
type Subscription struct {
    ID string
    CustomerID string
    PlanID string
    Status string  // 'active', 'past_due', 'cancelled'
    BillingPeriod string  // 'monthly', 'yearly'
}
```

---

## Implementation Flow

### MVP (Layer 1) - 4-6 weeks

**Week 1-2: Core Runtime**
- Firecracker driver
- Mock driver (dev without KVM)
- PostgreSQL schema
- Basic API (projects, deployments, VMs)
- Reconciliation engine

**Week 3-4: Build System**
- Git integration
- BuildKit integration
- OCI artifact handling
- Image catalog

**Week 5-6: AI Integration**
- AI role in auth
- /api/discover endpoint
- SDK (internal/sdk/)
- AI audit trail

### Production (Layer 2) - 4-6 weeks

**Week 1-2: Networking**
- Caddy integration
- IPAM
- TAP devices
- Gateway

**Week 3-4: Storage & Health**
- Volumes
- Health checks
- Restart policies
- Observability

**Week 5-6: Multi-Node**
- Scheduler
- Node management
- Replica management
- Scaling

### Platform (Layer 3) - 4-6 weeks

**Week 1-2: Environments & Secrets**
- Environment management
- Secret storage
- Variable injection

**Week 3-4: RBAC & Teams**
- Role-based access
- Team management
- Permissions

**Week 5-6: Advanced Features**
- Preview environments
- Rolling deployments
- Autoscaling

### Commercial (Layer 4) - 4-6 weeks

**Week 1-2: Billing Foundation**
- Products & plans
- Usage metering
- Invoicing

**Week 3-4: Subscriptions**
- Subscription lifecycle
- Payment processing
- Dunning

**Week 5-6: Marketplace**
- Extension system
- Marketplace UI
- Third-party integrations

---

## Success Criteria

### MVP Success
- ✅ Deploy from Git to Firecracker
- ✅ AI can call /api/discover and deploy
- ✅ Logs streaming works
- ✅ Single-node reliable
- ✅ Mock driver for dev

### Production Success
- ✅ Custom domains with HTTPS
- ✅ Persistent volumes
- ✅ Multi-node scheduling
- ✅ Observability complete
- ✅ 99.9% uptime

### Platform Success
- ✅ Environments (dev/staging/prod)
- ✅ Secrets management
- ✅ RBAC working
- ✅ Teams and organizations

### Commercial Success
- ✅ Billing and subscriptions
- ✅ Usage metering
- ✅ Payment processing
- ✅ Reseller support

---

## Repository Structure (New)

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
│   │   │   └── vms.go
│   │   ├── middleware/
│   │   │   ├── auth.go      # AI role handling
│   │   │   └── audit.go
│   │   └── routes.go
│   ├── sdk/                 # AI SDK
│   │   ├── client.go
│   │   ├── types.go
│   │   └── discover.go
│   ├── control/             # Reconciliation
│   │   ├── engine.go
│   │   └── scheduler.go
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

## Next Steps

1. **Review SRS v2.0** - approve or adjust
2. **Start MVP implementation** - Layer 1, Week 1
3. **Deliver MVP** - core + AI working
4. **Iterate** - add layers as needed

**Timeline to MVP**: 4-6 weeks  
**Timeline to Production**: 8-12 weeks  
**Timeline to Complete**: 16-24 weeks

---

## Philosophy

**"Make it work, make it right, make it fast."**

- **Make it work**: MVP (Layer 1) - deploy from Git to Firecracker
- **Make it right**: Production (Layer 2) - reliable, observable
- **Make it fast**: Platform (Layer 3) - scalable, automated

**Each layer is a releasable milestone.**

Stop at any layer if it meets your needs.
