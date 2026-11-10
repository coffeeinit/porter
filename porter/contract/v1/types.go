// Package v1 defines the versioned control-plane API and data contract.
// The root server exposes the HTTP adapter only with explicit PostgreSQL configuration.
package v1

import (
	"encoding/json"
	"time"

	"porter/protocol"
)

const (
	Version         = "v1"
	BasePath        = "/api/v1"
	DefaultPageSize = 50
	MaxPageSize     = 100
)

type ProjectState string

const (
	ProjectActive   ProjectState = "active"
	ProjectDeleting ProjectState = "deleting"
)

type ServiceState string

const (
	ServiceActive   ServiceState = "active"
	ServiceDeleting ServiceState = "deleting"
)

type ReleaseState string

const (
	ReleaseQueued     ReleaseState = "queued"
	ReleaseBlocked    ReleaseState = "blocked"
	ReleaseBuilding   ReleaseState = "building"
	ReleaseReady      ReleaseState = "ready"
	ReleaseDeploying  ReleaseState = "deploying"
	ReleaseActive     ReleaseState = "active"
	ReleaseFailed     ReleaseState = "failed"
	ReleaseSuperseded ReleaseState = "superseded"
)

type ReleaseIntent string

const (
	ReleaseIntentPreview    ReleaseIntent = "preview"
	ReleaseIntentProduction ReleaseIntent = "production"
)

type ReleaseOperationKind string

const (
	ReleaseOperationBuild     ReleaseOperationKind = "build"
	ReleaseOperationPromotion ReleaseOperationKind = "production_promotion"
)

type ReleaseOperationState string

const (
	ReleaseOperationBlocked   ReleaseOperationState = "blocked"
	ReleaseOperationRunning   ReleaseOperationState = "running"
	ReleaseOperationSucceeded ReleaseOperationState = "succeeded"
	ReleaseOperationFailed    ReleaseOperationState = "failed"
)

type ReleaseHealthGateStatus string

const (
	ReleaseHealthGateNotRun  ReleaseHealthGateStatus = "not_run"
	ReleaseHealthGatePending ReleaseHealthGateStatus = "pending"
	ReleaseHealthGatePassed  ReleaseHealthGateStatus = "passed"
	ReleaseHealthGateFailed  ReleaseHealthGateStatus = "failed"
)

type InstanceState string

const (
	InstancePending      InstanceState = "pending"
	InstanceProvisioning InstanceState = "provisioning"
	InstanceRunning      InstanceState = "running"
	InstanceStopped      InstanceState = "stopped"
	InstanceFailed       InstanceState = "failed"
	InstanceDeleting     InstanceState = "deleting"
	InstanceDeleted      InstanceState = "deleted"
)

type InstanceHealth string

const (
	HealthUnknown   InstanceHealth = "unknown"
	HealthHealthy   InstanceHealth = "healthy"
	HealthUnhealthy InstanceHealth = "unhealthy"
)

type Runtime string

// FirecrackerRuntime is the only production workload runtime in this contract.
const FirecrackerRuntime Runtime = "firecracker"

type FleetNodeState string

type AgentEnrollmentTarget string

const (
	FleetNodeReady    FleetNodeState = "ready"
	FleetNodeCordoned FleetNodeState = "cordoned"
	FleetNodeDraining FleetNodeState = "draining"
	FleetNodeOffline  FleetNodeState = "offline"

	AgentEnrollmentUnspecified AgentEnrollmentTarget = "unspecified"
	AgentEnrollmentSameHost    AgentEnrollmentTarget = "same_host"
	AgentEnrollmentRemoteHost  AgentEnrollmentTarget = "remote_host"
)

type SourceKind string

const SourceGit SourceKind = "git"

type BuildKind string

const (
	BuildDockerfile BuildKind = "dockerfile"
	BuildCompose    BuildKind = "compose"
)

type Project struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	State     ProjectState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type Service struct {
	ID               string       `json:"id"`
	ProjectID        string       `json:"project_id"`
	Name             string       `json:"name"`
	State            ServiceState `json:"state"`
	Runtime          Runtime      `json:"runtime"`
	Port             int          `json:"port"`
	VCPUs            int          `json:"vcpus"`
	MemoryMB         int          `json:"memory_mb"`
	DesiredInstances int          `json:"desired_instances"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// ReleaseSource.Ref is the requested branch/tag selector, not the pinned source identity.
type ReleaseSource struct {
	Kind       SourceKind `json:"kind"`
	Repository string     `json:"repository"`
	Ref        string     `json:"ref"`
}

type BuildSpec struct {
	Kind           BuildKind `json:"kind"`
	DockerfilePath string    `json:"dockerfile_path,omitempty"`
	ComposeFile    string    `json:"compose_file,omitempty"`
	PrimaryService string    `json:"primary_service,omitempty"`
}

type Release struct {
	ID              string                  `json:"id"`
	ServiceID       string                  `json:"service_id"`
	Sequence        int64                   `json:"sequence"`
	State           ReleaseState            `json:"state"`
	Intent          ReleaseIntent           `json:"intent"`
	Source          ReleaseSource           `json:"source"`
	ResolvedCommit  string                  `json:"resolved_commit,omitempty"`
	Build           BuildSpec               `json:"build"`
	ArtifactDigest  string                  `json:"artifact_digest,omitempty"`
	GuestReady      bool                    `json:"guest_ready"`
	GuestReadyAt    *time.Time              `json:"guest_ready_at,omitempty"`
	HealthGate      ReleaseHealthGateStatus `json:"health_gate_status"`
	HealthCheckedAt *time.Time              `json:"health_gate_checked_at,omitempty"`
	HealthGateError string                  `json:"health_gate_error,omitempty"`
	FailureMessage  string                  `json:"failure_message,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

// ReleaseOperation is a durable GitOps request record. A blocked operation is
// persisted intent only: it has not started a build or production activation.
type ReleaseOperation struct {
	ID             string                       `json:"id"`
	ServiceID      string                       `json:"service_id"`
	ReleaseID      string                       `json:"release_id"`
	Kind           ReleaseOperationKind         `json:"kind"`
	State          ReleaseOperationState        `json:"state"`
	Intent         ReleaseIntent                `json:"intent"`
	Blocker        string                       `json:"blocker"`
	RequestedRef   string                       `json:"requested_ref"`
	ResolvedCommit string                       `json:"resolved_commit,omitempty"`
	Checks         []ReleasePromotionCheck      `json:"checks,omitempty"`
	BuilderAttempt int                          `json:"builder_attempt,omitempty"`
	BuilderCommand string                       `json:"builder_command_id,omitempty"`
	BuilderResult  *protocol.BuilderBuildResult `json:"builder_result,omitempty"`
	BuilderError   string                       `json:"builder_error,omitempty"`
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
}

type ReleasePromotionCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Instance struct {
	ID        string         `json:"id"`
	ServiceID string         `json:"service_id"`
	ReleaseID string         `json:"release_id"`
	Ordinal   int            `json:"ordinal"`
	Runtime   Runtime        `json:"runtime"`
	State     InstanceState  `json:"state"`
	Health    InstanceHealth `json:"health"`
	NodeID    string         `json:"node_id,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type FleetNode struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	State            FleetNodeState        `json:"state"`
	EnrollmentTarget AgentEnrollmentTarget `json:"enrollment_target"`
	LastSeenAt       *time.Time            `json:"last_seen_at,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// CreateAgentEnrollmentTokenRequest selects where the operator intends to run
// the agent. The selection is recorded, not independently verified by the server.
type CreateAgentEnrollmentTokenRequest struct {
	Target          AgentEnrollmentTarget `json:"target"`
	ControlPlaneURL string                `json:"control_plane_url"`
}

// AgentEnrollmentTokenIssue contains a one-time secret and is returned only by
// the authenticated, no-store operator action that creates the token.
type AgentEnrollmentTokenIssue struct {
	ID              string                `json:"id"`
	Token           string                `json:"token"`
	Target          AgentEnrollmentTarget `json:"target"`
	ControlPlaneURL string                `json:"control_plane_url"`
	ExpiresAt       time.Time             `json:"expires_at"`
}

// FleetCapabilities reports which PostgreSQL-backed provisioning operations
// are mounted. The capability endpoint requires an operator Bearer.
type FleetCapabilities struct {
	JoinTokenIssuance      bool `json:"join_token_issuance"`
	AgentEnrollment        bool `json:"agent_enrollment"`
	NodeCredentialRotation bool `json:"node_credential_rotation"`
}

type DomainClaimState string

const (
	DomainClaimPendingProof DomainClaimState = "pending_proof"
	DomainClaimVerified     DomainClaimState = "verified"
	GatewayUnavailable                       = "unavailable"
)

// DomainClaim records ownership intent and the exact TXT proof observed by the
// control plane. Verification proves DNS TXT control only, not gateway reachability.
type DomainClaim struct {
	ID                     string           `json:"id"`
	ProjectID              string           `json:"project_id"`
	ServiceID              string           `json:"service_id"`
	Hostname               string           `json:"hostname"`
	State                  DomainClaimState `json:"state"`
	VerificationTXTName    string           `json:"verification_txt_name"`
	VerificationTXTValue   string           `json:"verification_txt_value"`
	OwnershipProofTXTValue string           `json:"ownership_proof_txt_value,omitempty"`
	OwnershipProvedAt      *time.Time       `json:"ownership_proved_at,omitempty"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

// GatewayRoute stores desired route configuration only. GatewayStatus is
// always unavailable until a real gateway reconciler is integrated.
type GatewayRoute struct {
	ID             string    `json:"id"`
	DomainClaimID  string    `json:"domain_claim_id"`
	ProjectID      string    `json:"project_id"`
	ServiceID      string    `json:"service_id"`
	Hostname       string    `json:"hostname"`
	PathPrefix     string    `json:"path_prefix"`
	DesiredEnabled bool      `json:"desired_enabled"`
	GatewayStatus  string    `json:"gateway_status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateDomainClaimRequest struct {
	Hostname string `json:"hostname"`
}

type CreateGatewayRouteRequest struct {
	PathPrefix string `json:"path_prefix"`
}

type SetGatewayRouteRequest struct {
	DesiredEnabled *bool `json:"desired_enabled"`
}

type VolumeAccessMode string

const (
	VolumeSingleWriter         VolumeAccessMode = "single_writer"
	VolumeMetadataOnly                          = "metadata_only"
	IntegrityNotAvailable                       = "not_available"
	SnapshotCaptureUnavailable                  = "unavailable"
	SnapshotCrashConsistent                     = "crash_consistent"
	BackupMetadataOnly                          = "metadata_only"
	BackupOffHostUnavailable                    = "unavailable"
)

// Volume stores a requested single-writer declaration only; it is not a
// provisioned disk and has not been attached to a Firecracker guest.
type Volume struct {
	ID                 string           `json:"id"`
	ProjectID          string           `json:"project_id"`
	ServiceID          string           `json:"service_id"`
	Name               string           `json:"name"`
	SizeMiB            int64            `json:"size_mib"`
	MountPath          string           `json:"mount_path,omitempty"`
	AccessMode         VolumeAccessMode `json:"access_mode"`
	State              string           `json:"state"`
	NodeAffinityID     *string          `json:"node_affinity_id,omitempty"`
	IntegrityStatus    string           `json:"integrity_status"`
	IntegritySHA256    *string          `json:"integrity_sha256,omitempty"`
	IntegrityCheckedAt *time.Time       `json:"integrity_checked_at,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

type CreateVolumeRequest struct {
	Name           string           `json:"name"`
	SizeMiB        int64            `json:"size_mib"`
	MountPath      string           `json:"mount_path,omitempty"`
	AccessMode     VolumeAccessMode `json:"access_mode"`
	NodeAffinityID string           `json:"node_affinity_id,omitempty"`
}

type SnapshotMetadata struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"project_id"`
	ServiceID          string     `json:"service_id"`
	InstanceID         string     `json:"instance_id"`
	Consistency        string     `json:"consistency"`
	CaptureStatus      string     `json:"capture_status"`
	CaptureOperationID *string    `json:"capture_operation_id,omitempty"`
	RestoreStatus      string     `json:"restore_status"`
	RestoreOperationID *string    `json:"restore_operation_id,omitempty"`
	NodeAffinityID     *string    `json:"node_affinity_id,omitempty"`
	ArtifactSHA256     *string    `json:"artifact_sha256,omitempty"`
	SizeBytes          *int64     `json:"size_bytes,omitempty"`
	IntegrityStatus    string     `json:"integrity_status"`
	IntegrityCheckedAt *time.Time `json:"integrity_checked_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

type CreateSnapshotMetadataRequest struct {
	Consistency string `json:"consistency"`
}

type SnapshotOperationRequest struct {
	Consistency    string `json:"consistency,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type SnapshotOperationResponse struct {
	Snapshot  SnapshotMetadata `json:"snapshot"`
	Operation Operation        `json:"operation"`
}

type BackupMetadata struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"project_id"`
	ServiceID          string     `json:"service_id"`
	InstanceID         string     `json:"instance_id"`
	SnapshotID         string     `json:"snapshot_id"`
	Consistency        string     `json:"consistency"`
	State              string     `json:"state"`
	OffHostStatus      string     `json:"off_host_status"`
	NodeAffinityID     *string    `json:"node_affinity_id,omitempty"`
	ArtifactSHA256     *string    `json:"artifact_sha256,omitempty"`
	SizeBytes          *int64     `json:"size_bytes,omitempty"`
	IntegrityStatus    string     `json:"integrity_status"`
	IntegrityCheckedAt *time.Time `json:"integrity_checked_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

type CreateBackupMetadataRequest struct {
	Consistency string `json:"consistency"`
}

type CreateProjectRequest struct {
	Name string `json:"name"`
}

type CreateServiceRequest struct {
	Name             string  `json:"name"`
	Port             int     `json:"port"`
	VCPUs            int     `json:"vcpus"`
	MemoryMB         int     `json:"memory_mb"`
	DesiredInstances int     `json:"desired_instances"`
	Runtime          Runtime `json:"runtime"`
}

type CreateReleaseRequest struct {
	Source         ReleaseSource `json:"source"`
	Build          BuildSpec     `json:"build"`
	Intent         ReleaseIntent `json:"intent,omitempty"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
}

type ReleasePromotionRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type ListQuery struct {
	PageSize int    `json:"page_size,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// TelemetryLog is a redacted agent/runtime log record. It is not application
// stdout/stderr and does not imply distributed tracing support.
type TelemetryLog struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	ProjectID  string    `json:"project_id,omitempty"`
	ServiceID  string    `json:"service_id,omitempty"`
	ReportedAt time.Time `json:"reported_at"`
	ReceivedAt time.Time `json:"received_at"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
}

// TelemetryMetric is a numeric measurement from an authenticated v2 agent.
type TelemetryMetric struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	ProjectID  string    `json:"project_id,omitempty"`
	ServiceID  string    `json:"service_id,omitempty"`
	ReportedAt time.Time `json:"reported_at"`
	ReceivedAt time.Time `json:"received_at"`
	Name       string    `json:"name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit,omitempty"`
}

type ErrorCode string

const (
	ErrorBadRequest         ErrorCode = "bad_request"
	ErrorUnauthenticated    ErrorCode = "unauthenticated"
	ErrorPermissionDenied   ErrorCode = "permission_denied"
	ErrorNotFound           ErrorCode = "not_found"
	ErrorConflict           ErrorCode = "conflict"
	ErrorFailedPrecondition ErrorCode = "failed_precondition"
	ErrorRateLimited        ErrorCode = "rate_limited"
	ErrorInternal           ErrorCode = "internal"
)

type ErrorDetail struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	RequestID string    `json:"request_id,omitempty"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type OperationState string

const (
	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
)

type AgentCommandKind string

const (
	AgentCommandVMApply         AgentCommandKind = "vm.apply"
	AgentCommandVMDelete        AgentCommandKind = "vm.delete"
	AgentCommandVMStart         AgentCommandKind = "vm.start"
	AgentCommandVMStop          AgentCommandKind = "vm.stop"
	AgentCommandVMRestart       AgentCommandKind = "vm.restart"
	AgentCommandSnapshotCreate  AgentCommandKind = "snapshot.create"
	AgentCommandSnapshotRestore AgentCommandKind = "snapshot.restore"
	AgentCommandVolumeResize    AgentCommandKind = "volume.resize"
)

type AgentCommandState string

const (
	AgentCommandQueued    AgentCommandState = "queued"
	AgentCommandLeased    AgentCommandState = "leased"
	AgentCommandSucceeded AgentCommandState = "succeeded"
	AgentCommandFailed    AgentCommandState = "failed"
)

type AgentCommandResultState string

const (
	CommandResultRunning       AgentCommandResultState = "running"
	CommandResultStopped       AgentCommandResultState = "stopped"
	CommandResultDeleted       AgentCommandResultState = "deleted"
	CommandResultSnapshotReady AgentCommandResultState = "snapshot_ready"
	CommandResultRestored      AgentCommandResultState = "restored"
	CommandResultResized       AgentCommandResultState = "resized"
	CommandResultFailed        AgentCommandResultState = "failed"
)

// Operation is an operator-safe summary. It never includes a lease token or
// command payload, which may contain sensitive launch configuration.
type Operation struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id"`
	ServiceID      string            `json:"service_id"`
	InstanceID     string            `json:"instance_id"`
	ResourceID     string            `json:"resource_id"`
	Kind           AgentCommandKind  `json:"kind"`
	State          OperationState    `json:"state"`
	Error          string            `json:"error,omitempty"`
	CommandID      string            `json:"command_id"`
	CommandState   AgentCommandState `json:"command_state"`
	Attempt        int               `json:"attempt"`
	LeaseExpiresAt *time.Time        `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// AgentCommandLease is an internal agent-contract envelope. The new API does
// not expose a polling route until the agent protocol and authentication are
// upgraded to support its lease-token fencing.
type AgentCommandLease struct {
	ID             string           `json:"id"`
	NodeID         string           `json:"node_id"`
	OperationID    string           `json:"operation_id"`
	Attempt        int              `json:"attempt"`
	LeaseToken     string           `json:"lease_token"`
	LeaseExpiresAt time.Time        `json:"lease_expires_at"`
	Kind           AgentCommandKind `json:"kind"`
	Payload        json.RawMessage  `json:"payload"`
	CreatedAt      time.Time        `json:"created_at"`
}

// AgentCommandResult is accepted by the persistence store only; it is not
// currently exposed as an unauthenticated or agent-facing HTTP route.
type AgentCommandResult struct {
	CommandID   string                  `json:"command_id"`
	OperationID string                  `json:"operation_id"`
	NodeID      string                  `json:"node_id"`
	Kind        AgentCommandKind        `json:"kind"`
	Attempt     int                     `json:"attempt"`
	LeaseToken  string                  `json:"lease_token"`
	Success     bool                    `json:"success"`
	State       AgentCommandResultState `json:"state"`
	Error       string                  `json:"error,omitempty"`
}

type Event struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	OccurredAt     time.Time       `json:"occurred_at"`
	ProjectID      string          `json:"project_id,omitempty"`
	ServiceID      string          `json:"service_id,omitempty"`
	ReleaseID      string          `json:"release_id,omitempty"`
	InstanceID     string          `json:"instance_id,omitempty"`
	FleetNodeID    string          `json:"fleet_node_id,omitempty"`
	DomainClaimID  string          `json:"domain_claim_id,omitempty"`
	GatewayRouteID string          `json:"gateway_route_id,omitempty"`
	VolumeID       string          `json:"volume_id,omitempty"`
	SnapshotID     string          `json:"snapshot_id,omitempty"`
	BackupID       string          `json:"backup_id,omitempty"`
	OperationID    string          `json:"operation_id,omitempty"`
	CommandID      string          `json:"command_id,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
}
