// Package types defines the canonical Porter domain types shared across
// the API, store, VM manager, and dashboard.
package types

import (
	"fmt"
	"time"

	"porter/internal/resource"
)

// VM lifecycle states. Canonical values live in resource (single source of
// truth per T7b); these aliases keep the store-facing types stable.
const (
	StatePending  = resource.StatePending
	StateBooting  = resource.StateBooting
	StateRunning  = resource.StateRunning
	StateStopping = resource.StateStopping
	StateStopped  = resource.StateStopped
	StateFailed   = resource.StateFailed
	StateDeleting = resource.StateDeleting
	StateDeleted  = resource.StateDeleted
)

const (
	HealthHealthy   = resource.HealthHealthy
	HealthUnhealthy = resource.HealthUnhealthy
	HealthChecking  = resource.HealthChecking
	HealthDegraded  = resource.HealthDegraded
	HealthUnknown   = resource.HealthUnknown
)

// Workload types (WP1): every project runs as an ephemeral microVM pool by
// default; a project flipped to VPS mode is persistent — its replica pool
// never scales to zero and its IP allocation stays pinned to the VM.
const (
	WorkloadTypeMicroVM = "microvm"
	WorkloadTypeVPS     = "vps"
)

type Port struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"` // host->guest mapping; defaults to container_port
	Protocol      string `json:"protocol"`
}

// ImageManifest is one entry in the on-disk direct Firecracker image catalog.
// A deployable manifest points rootfs at an ext4 image and kernel at a vmlinux
// file on the host. Image is the stable catalog reference exposed to the API.
type ImageManifest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Description  string            `json:"description"`
	Image        string            `json:"image,omitempty"`  // stable catalog reference
	Rootfs       string            `json:"rootfs,omitempty"` // bare ext4 rootfs path on the host
	Kernel       string            `json:"kernel,omitempty"` // optional per-image vmlinux
	Architecture string            `json:"architecture,omitempty"`
	RootfsSHA256 string            `json:"rootfs_sha256,omitempty"`
	KernelSHA256 string            `json:"kernel_sha256,omitempty"`
	Status       string            `json:"status,omitempty"` // ready | remote | missing | invalid
	VCPUs        int               `json:"vcpus"`
	MemMiB       int               `json:"mem_mib"`
	Ports        []Port            `json:"ports"`
	Env          map[string]string `json:"env"`
	Tags         []string          `json:"tags"`
	Logo         string            `json:"logo"` // URL; the UI renders an offline fallback if it can't load

	// Remote catalog fields. An entry published in the upstream image catalog
	// names where its artifacts live instead of carrying a local path. Porter
	// keeps only a local cache: it downloads the artifact into ImagesDir on
	// first use and then fills Rootfs/Kernel with the local path, Status moving
	// from "remote" to "ready". Updating an image upstream is therefore an edit
	// to the catalog JSON, not a new Porter release.
	RootfsURL  string `json:"rootfs_url,omitempty"`
	KernelURL  string `json:"kernel_url,omitempty"`
	RootfsSize int64  `json:"rootfs_size,omitempty"`
	KernelSize int64  `json:"kernel_size,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Publisher  string `json:"publisher,omitempty"`
}

type Healthcheck struct {
	Type        string `json:"type,omitempty"`
	Path        string `json:"path,omitempty"`
	Port        int    `json:"port,omitempty"`
	IntervalSec int    `json:"interval_sec,omitempty"`
}

type VM struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ProjectID         string            `json:"project_id"`
	ServiceName       string            `json:"service_name"`
	State             string            `json:"state"`
	HealthStatus      string            `json:"health_status"`
	ReplicaIndex      int               `json:"replica_index"`
	Image             string            `json:"image"`
	RootfsPath        string            `json:"rootfs_path,omitempty"`
	Kernel            string            `json:"kernel,omitempty"` // per-VM vmlinux (custom images); falls back to the shared kernel
	ContainerID       string            `json:"container_id,omitempty"`
	TaskID            string            `json:"task_id,omitempty"`
	VCPUs             int               `json:"vcpus"`
	MemMiB            int               `json:"mem_mib"`
	IPAddress         string            `json:"ip_address"`
	WorkloadType      string            `json:"workload_type,omitempty"` // microvm (default) | vps
	Persistent        bool              `json:"persistent,omitempty"`    // VPS mode: never torn down by ephemeral paths
	Ports             []Port            `json:"ports"`
	Env               map[string]string `json:"env,omitempty"`
	VolumeID          string            `json:"volume_id,omitempty"` // persistent volume attached as /dev/vdb
	Healthcheck       *Healthcheck      `json:"healthcheck,omitempty"`
	Restart           string            `json:"restart,omitempty"`
	Error             string            `json:"error,omitempty"`
	Crashed           bool              `json:"crashed,omitempty"`
	SnapshotPath      string            `json:"snapshot_path,omitempty"`
	SnapshotMemPath   string            `json:"snapshot_mem_path,omitempty"`
	DeploymentID      string            `json:"deployment_id,omitempty"`
	DeploymentVersion string            `json:"deployment_version,omitempty"`
	DeploymentEnv     string            `json:"deployment_environment,omitempty"`
	GuestBase         string            `json:"guest_base,omitempty"`
	SnapshotStatus    string            `json:"snapshot_status,omitempty"` // none | creating | ready | restoring | failed
	SnapshotError     string            `json:"snapshot_error,omitempty"`
	SnapshotCreatedAt *time.Time        `json:"snapshot_created_at,omitempty"`
	LastRecoveredAt   *time.Time        `json:"last_recovered_at,omitempty"`
	RecoveryCount     int               `json:"recovery_count,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
}

// ContainerPort returns the first declared container port (the port the app
// listens on inside the VM), or 0 when none is declared.
func (v *VM) ContainerPort() int {
	for _, p := range v.Ports {
		if p.ContainerPort != 0 {
			return p.ContainerPort
		}
	}
	return 0
}

type ServicePool struct {
	Desired int      `json:"desired"`
	Healthy int      `json:"healthy"`
	VMs     []string `json:"vms"`
}

type Project struct {
	ID              string                  `json:"id"`
	OrgID           string                  `json:"org_id,omitempty"` // always set; resolves to the user's default org at create
	Name            string                  `json:"name"`
	Source          string                  `json:"source"`
	Image           string                  `json:"image,omitempty"` // OCI ref (plain text, never a local path)
	Network         string                  `json:"network"`
	VMIDs           []string                `json:"vm_ids"`
	ServicePools    map[string]*ServicePool `json:"service_pools,omitempty"`
	ComposeYAML     string                  `json:"-"`
	HostMountPath   string                  `json:"host_mount_path,omitempty"`  // optional bind mount (no managed volumes)
	ReplicasDesired int                     `json:"replicas_desired,omitempty"` // replica pool size (>=1)
	RestartPolicy   string                  `json:"restart_policy,omitempty"`
	Healthcheck     *Healthcheck            `json:"healthcheck,omitempty"`
	Env             map[string]string       `json:"env,omitempty"`
	Tags            []string                `json:"tags,omitempty"`
	SSHEnabled      bool                    `json:"ssh_enabled,omitempty"`     // SSH off by default; user turns it on per project
	Replicas        int                     `json:"replicas,omitempty"`        // alias for replica pool size
	StackID         string                  `json:"stack_id,omitempty"`        // parent compose stack, if this project is a compose service
	ComposeService  string                  `json:"compose_service,omitempty"` // service name inside its stack
	Model           string                  `json:"model,omitempty"`           // ML model ref (gpu/batch serving)
	GPU             string                  `json:"gpu,omitempty"`             // e.g. "nvidia-t4" or ""
	Networks        []string                `json:"networks,omitempty"`        // per-project bridge networks
	Autoscale       *AutoscalePolicy        `json:"autoscale,omitempty"`       // horizontal autoscaling policy
	CreatedBy       string                  `json:"created_by,omitempty"`      // username of the creator (ownership without a role)
	WorkloadType    string                  `json:"workload_type,omitempty"`   // microvm (default) | vps; empty reads as microvm
	Persistent      bool                    `json:"persistent,omitempty"`      // VPS mode: replica pool never scales to zero
	CreatedAt       time.Time               `json:"created_at"`
}

// ReplicaFloor is the minimum replica count this project's pool may converge
// to. Persistent (VPS) workloads never scale to zero — their floor is 1 — so
// controllers clamp desired state back up instead of running the ephemeral
// scale-to-zero teardown on the last replica.
func (p *Project) ReplicaFloor() int {
	if p.WorkloadType == WorkloadTypeVPS || p.Persistent {
		return 1
	}
	return 0
}

// AutoscalePolicy controls horizontal autoscaling for a project. The autoscaler
// polls recent load and scales the replica pool between Min and Max.
type AutoscalePolicy struct {
	MinReplicas  int     `json:"min_replicas"`
	MaxReplicas  int     `json:"max_replicas"`
	MinMemMiB    int     `json:"min_mem_mib"`            // per-replica memory floor (0 = unset)
	MaxMemMiB    int     `json:"max_mem_mib"`            // per-replica memory ceiling (0 = unset)
	TargetCPU    float64 `json:"target_cpu_percent"`     // scale up when avg CPU exceeds this
	ScaleDownCPU float64 `json:"scale_down_cpu_percent"` // scale down when avg CPU below this (0 disables)
	CooldownSec  int     `json:"cooldown_seconds"`       // min seconds between scale events
	Enabled      bool    `json:"enabled"`
}

// Validate gates autoscale policies: replica and memory ranges must hold.
func (p AutoscalePolicy) Validate() error {
	if p.MaxReplicas <= 0 {
		return fmt.Errorf("autoscale: max replicas must be positive")
	}
	if p.MinReplicas < 0 || p.MinReplicas > p.MaxReplicas {
		return fmt.Errorf("autoscale: min replicas must be within [0, max]")
	}
	if p.MaxMemMiB < 0 || p.MinMemMiB < 0 {
		return fmt.Errorf("autoscale: memory bounds must be non-negative")
	}
	if p.MaxMemMiB > 0 && p.MinMemMiB > p.MaxMemMiB {
		return fmt.Errorf("autoscale: min memory above max memory")
	}
	return nil
}

type Domain struct {
	ProjectID string `json:"project_id,omitempty"`
	VMID      string `json:"-"`
	Domain    string `json:"domain"`
	Type      string `json:"type"`
	Status    string `json:"status"`
}

type TrafficEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	ProjectID  string    `json:"project_id,omitempty"` // set by producers that know the project (gateway)
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS int       `json:"duration_ms"`
	RemoteIP   string    `json:"remote_ip"`
	BytesIn    int64     `json:"bytes_in,omitempty"`  // request body bytes
	BytesOut   int64     `json:"bytes_out,omitempty"` // response body bytes
}

// WebVital is a real browser-reported Core Web Vital (LCP/CLS/INP/TTFB) beacon.
type WebVital struct {
	ProjectID string    `json:"project_id"`
	Path      string    `json:"path"`
	Metric    string    `json:"metric"` // lcp | cls | inp | ttfb
	Value     float64   `json:"value"`
	Rating    string    `json:"rating,omitempty"` // good | needs-improvement | poor
	Timestamp time.Time `json:"timestamp"`
}

// User is a database-backed account. The initial admin row is seeded by a
// migration and initialized once from an environment secret; every account
// then participates in the same persisted RBAC model. Passwords are stored as
// salted hashes.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	Email        string    `json:"email,omitempty"`
	NotifyOptIn  bool      `json:"notify_opt_in,omitempty"`
	PasswordHash string    `json:"-"`
	Salt         string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Volume is a persistent storage volume (Phase 7 / v0.1.0 fold-in) that a VM
// can attach at create time. For v0.1.0 it maps to a host directory under the
// state dir; snapshots/backups/object-storage are later versions.
type Volume struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"`
	Name      string    `json:"name"`
	SizeMiB   int       `json:"size_mib"`
	Path      string    `json:"path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Server is a registered compute host in a Porter cluster (Phase 8 scaffold).
// v0.1.0 only registers hosts; scheduling/migration stay deferred.
type Server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Status    string    `json:"status"`
	VCPUs     int       `json:"vcpus"`
	MemMiB    int       `json:"mem_mib"`
	OS        string    `json:"os,omitempty"`
	Arch      string    `json:"arch,omitempty"`
	Version   string    `json:"version,omitempty"`
	Projects  int       `json:"projects"`
	VMs       int       `json:"vms"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ServerHeartbeat carries a worker node's status report to the control plane.
type ServerHeartbeat struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Status   string `json:"status"`
	VCPUs    int    `json:"vcpus"`
	MemMiB   int    `json:"mem_mib"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Projects int    `json:"projects"`
	VMs      int    `json:"vms"`
}

// ServerSSH describes how an operator reaches a node over SSH (via the Porter
// SSH gateway or direct host when reachable).
type ServerSSH struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	KeyName string `json:"key_name,omitempty"`
	Gateway string `json:"gateway,omitempty"` // e.g. "porter" | "direct"
}

// Service is one compose service (or the synthetic service created for a
// single-image deploy). One VM per replica.
type Service struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Name            string            `json:"name"`
	Image           string            `json:"image"`
	VCPUs           int               `json:"vcpus"`
	MemMiB          int               `json:"mem_mib"`
	ReplicasDesired int               `json:"replicas_desired"`
	RestartPolicy   string            `json:"restart_policy"`
	Healthcheck     *Healthcheck      `json:"healthcheck,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	DependsOn       []string          `json:"depends_on,omitempty"`
	Ports           []Port            `json:"ports"`
	CreatedAt       time.Time         `json:"created_at"`
}

// GoldenImage is a reusable direct-Firecracker VM template. `Image` is a
// stable catalog reference such as custom://redis; Rootfs and Kernel identify
// the host artifacts required to boot the microVM.
type GoldenImage struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Description  string            `json:"description"`
	VCPUs        int               `json:"vcpus"`
	MemMiB       int               `json:"mem_mib"`
	Ports        []Port            `json:"ports"`
	Env          map[string]string `json:"env,omitempty"`
	Tags         []string          `json:"tags"`
	Logo         string            `json:"logo"` // image URL for the dashboard tile
	Version      string            `json:"version"`
	Kind         string            `json:"kind,omitempty"`   // direct | custom (user-uploaded microVM)
	Rootfs       string            `json:"rootfs,omitempty"` // host path to ext4 rootfs (custom)
	Kernel       string            `json:"kernel,omitempty"` // host path to vmlinux (custom)
	Architecture string            `json:"architecture,omitempty"`
	RootfsSHA256 string            `json:"rootfs_sha256,omitempty"`
	KernelSHA256 string            `json:"kernel_sha256,omitempty"`
	Status       string            `json:"status,omitempty"` // ready | missing | invalid
	ValidatedAt  *time.Time        `json:"validated_at,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// Deployment is one revision of a project (v0.3 version history / rollback).
type Deployment struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id"`
	BuildID        string            `json:"build_id,omitempty"`
	Revision       int               `json:"revision"`
	VersionLabel   string            `json:"version_label,omitempty"`
	GuestBase      string            `json:"guest_base,omitempty"`
	Environment    string            `json:"environment,omitempty"` // preview | staging | production
	IsProduction   bool              `json:"is_production"`
	RouteWeight    int               `json:"route_weight"` // percentage of production traffic
	VMIDs          []string          `json:"vm_ids,omitempty"`
	GitURL         string            `json:"git_url,omitempty"`
	GitCommit      string            `json:"git_commit,omitempty"`
	BuildStatus    string            `json:"build_status"`
	ImageDigest    string            `json:"image_digest,omitempty"`
	RollbackTo     string            `json:"rollback_to,omitempty"`
	Checks         []DeploymentCheck `json:"checks,omitempty"`          // required checks gate promotion
	RolloutPercent int               `json:"rollout_percent,omitempty"` // legacy alias for RouteWeight
	PreviewURL     string            `json:"preview_url,omitempty"`     // routable preview host (gateway matches exact)
	CreatedAt      time.Time         `json:"created_at"`
}

// DeploymentCheck is one required check a deployment must pass before promote.
type DeploymentCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pending | running | passed | failed
	Detail string `json:"detail,omitempty"`
}

// Secret is an encrypted per-project secret (v0.2). Value is stored as an
// opaque blob; encryption happens in the API layer.
type Secret struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Name           string    `json:"name"`
	ValueEncrypted []byte    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MetricSample is one point in the v0.9 metrics time series.
type MetricSample struct {
	ID     string    `json:"id"`
	VMID   string    `json:"vm_id"`
	Metric string    `json:"metric"`
	Value  float64   `json:"value"`
	TS     time.Time `json:"ts"`
}

// HealthEvent is one recorded health transition for a VM (v0.9).
type HealthEvent struct {
	ID        string    `json:"id"`
	VMID      string    `json:"vm_id"`
	ProjectID string    `json:"project_id,omitempty"`
	ServiceID string    `json:"service_id,omitempty"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
	TS        time.Time `json:"ts"`
}

// Org is a group that projects belong to (project = VM, grouped into orgs).
// Every user auto-gets a default org (is_default=true) on first run/signup.
type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id,omitempty"`
	IsDefault bool      `json:"is_default,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgMember is a persisted organization membership and its organization-scoped role.
type OrgMember struct {
	OrgID    string `json:"org_id"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Group is a lightweight folder for related projects, always scoped to an org.
type Group struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// DNSRecord is one DNS entry attached to a project (project = VM).
type DNSRecord struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Value     string    `json:"value"`
	TTL       int       `json:"ttl"`
	CreatedAt time.Time `json:"created_at"`
}

// Stack is a docker-compose project: a named group of service-projects. Each
// compose service is its OWN project (own microVM pool), grouped under a stack.
type Stack struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OrgID       string    `json:"org_id,omitempty"`
	Source      string    `json:"source"`
	ComposeYAML string    `json:"compose_yaml,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// APIKey is a long-lived per-user token for programmatic access.
type APIKey struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Name      string     `json:"name"`
	TokenHash string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used_at,omitempty"`
}

// Alert is a threshold rule over a project metric.
type Alert struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Metric    string    `json:"metric"`
	Threshold float64   `json:"threshold"`
	Op        string    `json:"op"`
	CooldownS int       `json:"cooldown_s"`
	Silenced  bool      `json:"silenced"`
	CreatedAt time.Time `json:"created_at"`
}

// Feedback is a user feedback submission (dashboard "Send feedback" form).
type Feedback struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject,omitempty"`
	Message   string    `json:"message"`
	Category  string    `json:"category,omitempty"`
	Username  string    `json:"username,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Hook is an outbound webhook fired on project lifecycle events.
type Hook struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// Cron is a scheduled job that boots `job_image` as a short-lived microVM.
type Cron struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule"`
	JobImage  string     `json:"job_image"`
	Active    bool       `json:"active"`
	LastRun   *time.Time `json:"last_run_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Drain ships a project's logs/events to an external endpoint.
type Drain struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	Kind      string    `json:"kind"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// Redirect is a domain redirect rule for a project.
type Redirect struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Source    string    `json:"source"`
	Target    string    `json:"target"`
	Permanent bool      `json:"permanent"`
	CreatedAt time.Time `json:"created_at"`
}

// FirewallRule is an ingress/egress allow/deny rule for a project.
type FirewallRule struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Direction string    `json:"direction"`
	Action    string    `json:"action"`
	Proto     string    `json:"proto"`
	Ports     string    `json:"ports"`
	Source    string    `json:"source"`
	Priority  int       `json:"priority"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// Environment is a deploy environment (prod/staging/preview, Vercel-style).
type Environment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Branch    string    `json:"branch"`
	URL       string    `json:"url"`
	EnvDomain string    `json:"env_domain"`
	CreatedAt time.Time `json:"created_at"`
}

// Build is a git-based build that produces an OCI image for a microVM deploy.
type Build struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	GitURL       string    `json:"git_url"`
	Branch       string    `json:"branch"`
	GitCommit    string    `json:"git_commit,omitempty"`
	BuildStatus  string    `json:"build_status"`
	Image        string    `json:"image"`
	Log          string    `json:"log"`
	CreatedAt    time.Time `json:"created_at"`
}

// Network is a per-project bridge network (docker-ecosystem parity).
type Network struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"`
	Driver    string    `json:"driver"`
	CreatedAt time.Time `json:"created_at"`
}

// ProjectMember is a membership/role row for a project team.
type ProjectMember struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email,omitempty"` // invite-only members have no UserID yet
	Role      string    `json:"role"`
	Invited   bool      `json:"invited"`
	CreatedAt time.Time `json:"created_at"`
}

// Role is one RBAC role with the permission codes it grants (from
// role_permissions). Roles are defined in the DB so they are editable.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	// Policy flags (migration 0023): roles are data, never code constants.
	Scope     string `json:"scope,omitempty"` // platform|org|project|shared
	IsAdmin   bool   `json:"is_admin,omitempty"`
	SeesAll   bool   `json:"sees_all,omitempty"`
	IsDefault bool   `json:"is_default,omitempty"`
	IsSystem  bool   `json:"is_system,omitempty"`
}

// Permission is a single capability code (e.g. "ssh.connect").
type Permission struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
