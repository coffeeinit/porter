// Package resource defines the canonical Porter domain types.
package resource

import "time"

// Deployment is one revision of a project with version history and rollback support.
type Deployment struct {
	Metadata `json:"metadata"`
	Spec     DeploymentSpec   `json:"spec"`
	Status   DeploymentStatus `json:"status"`
}

func (d *Deployment) GetMetadata() *Metadata { return &d.Metadata }
func (d *Deployment) GetSpec() interface{}   { return d.Spec }
func (d *Deployment) GetStatus() *Status     { return &d.Status.Status }
func (d *Deployment) SetStatus(s *Status)    { d.Status.Status = *s }
func (d *Deployment) GetKind() string        { return KindDeployment }

// DeploymentSpec is the desired state of a deployment
type DeploymentSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id"`

	// Revision is the deployment revision number
	Revision int `json:"revision"`

	// VersionLabel is a human-readable version
	VersionLabel string `json:"version_label,omitempty"`

	// GuestBase is the base image reference
	GuestBase string `json:"guest_base,omitempty"`

	// Environment is the deployment environment (preview, staging, production)
	Environment string `json:"environment,omitempty"`

	// IsProduction indicates if this is a production deployment
	IsProduction bool `json:"is_production"`

	// Replicas is the desired replica count for rollout sizing
	Replicas int `json:"replicas"`

	// RouteWeight is the percentage of production traffic (0-100)
	RouteWeight int `json:"route_weight"`

	// GitURL for git-based deployments
	GitURL string `json:"git_url,omitempty"`

	// GitCommit is the git commit SHA
	GitCommit string `json:"git_commit,omitempty"`

	// ImageDigest is the OCI image digest
	ImageDigest string `json:"image_digest,omitempty"`

	// Image is the OCI image reference
	Image string `json:"image,omitempty"`

	// RollbackTo is the deployment ID to rollback to
	RollbackTo string `json:"rollback_to,omitempty"`

	// Checks are required checks that gate promotion
	Checks []DeploymentCheck `json:"checks,omitempty"`

	// RolloutPercent legacy alias for RouteWeight
	RolloutPercent int `json:"rollout_percent,omitempty"`

	// Strategy is the deployment strategy (recreate, rolling, blue-green, canary)
	Strategy string `json:"strategy,omitempty"`

	// MaxSurge is the maximum number of replicas above desired during rollout
	MaxSurge int `json:"max_surge,omitempty"`

	// MaxUnavailable is the maximum number of replicas unavailable during rollout
	MaxUnavailable int `json:"max_unavailable,omitempty"`
}

// DeploymentStatus is the observed state of a deployment
type DeploymentStatus struct {
	Status `json:",inline"`

	// BuildStatus is the build phase status
	BuildStatus string `json:"build_status"`

	// LastTransitionTime is when the current phase was entered
	LastTransitionTime time.Time `json:"last_transition_time,omitempty"`

	// VMIDs are the replica IDs for this deployment
	VMIDs []string `json:"vm_ids,omitempty"`

	// CurrentReplicas is the current number of replicas
	CurrentReplicas int `json:"current_replicas"`

	// UpdatedReplicas is the number of replicas updated to this revision
	UpdatedReplicas int `json:"updated_replicas"`

	// ReadyReplicas is the number of ready replicas
	ReadyReplicas int `json:"ready_replicas"`

	// AvailableReplicas is the number of available replicas
	AvailableReplicas int `json:"available_replicas"`

	// UnavailableReplicas is the number of unavailable replicas
	UnavailableReplicas int `json:"unavailable_replicas"`

	// RouteWeight is the observed traffic weight during promotion
	RouteWeight int `json:"route_weight,omitempty"`

	// RollbackTo is the deployment ID this deployment rolled back to
	RollbackTo string `json:"rollback_to,omitempty"`

	// ObservedGeneration is the last observed spec generation
	ObservedGeneration int64 `json:"observed_generation"`
}

// DeploymentCheck is one required check a deployment must pass before promotion
type DeploymentCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pending, running, passed, failed
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
}
