// Package resource defines the canonical Porter domain types.
package resource

// Project is a top-level application container. In the single-operator model,
// a project maps directly to a microVM application with a replica pool.
// In the multi-tenant model, projects belong to organizations and teams.
type Project struct {
	Metadata `json:"metadata"`
	Spec     ProjectSpec   `json:"spec"`
	Status   ProjectStatus `json:"status"`
}

func (p *Project) GetMetadata() *Metadata { return &p.Metadata }
func (p *Project) GetSpec() interface{}   { return p.Spec }
func (p *Project) GetStatus() *Status     { return &p.Status.Status }
func (p *Project) SetStatus(s *Status)    { p.Status.Status = *s }
func (p *Project) GetKind() string        { return KindProject }

// ProjectSpec is the desired state of a project
type ProjectSpec struct {
	// Source is the deployment source (git, image, compose)
	Source string `json:"source"`

	// Image is the OCI image reference for direct image deploys
	Image string `json:"image,omitempty"`

	// GitURL for git-based deployments
	GitURL string `json:"git_url,omitempty"`

	// GitBranch for git-based deployments
	GitBranch string `json:"git_branch,omitempty"`

	// Network is the project network name
	Network string `json:"network,omitempty"`

	// Networks is the list of project networks
	Networks []string `json:"networks,omitempty"`

	// ReplicasDesired is the desired replica pool size
	ReplicasDesired int `json:"replicas_desired,omitempty"`

	// Replicas is an alias for ReplicasDesired
	Replicas int `json:"replicas,omitempty"`

	// RestartPolicy for the project's replicas
	RestartPolicy string `json:"restart_policy,omitempty"`

	// Healthcheck configuration
	Healthcheck *Healthcheck `json:"healthcheck,omitempty"`

	// Environment variables
	Env map[string]string `json:"env,omitempty"`

	// Tags for organization
	Tags []string `json:"tags,omitempty"`

	// SSHEnabled enables SSH gateway access
	SSHEnabled bool `json:"ssh_enabled,omitempty"`

	// Autoscale configuration
	Autoscale *AutoscalePolicy `json:"autoscale,omitempty"`

	// HostMountPath optional bind mount (no managed volumes in v0.1)
	HostMountPath string `json:"host_mount_path,omitempty"`

	// StackID parent compose stack, if this project is a compose service
	StackID string `json:"stack_id,omitempty"`

	// ComposeService service name inside its stack
	ComposeService string `json:"compose_service,omitempty"`

	// Model ML model reference (gpu/batch serving)
	Model string `json:"model,omitempty"`

	// GPU type (e.g., "nvidia-t4")
	GPU string `json:"gpu,omitempty"`
}

// ProjectStatus is the observed state of a project
type ProjectStatus struct {
	Status `json:",inline"`

	// ServicePools is the map of service name -> pool status
	ServicePools map[string]*ServicePool `json:"service_pools,omitempty"`

	// URL is the public URL for the project
	URL string `json:"url,omitempty"`

	// LastDeploymentID is the ID of the most recent deployment
	LastDeploymentID string `json:"last_deployment_id,omitempty"`

	// Replicas is the current replica count
	Replicas int `json:"replicas,omitempty"`

	// HealthyReplicas is the count of healthy replicas
	HealthyReplicas int `json:"healthy_replicas,omitempty"`
}

// ServicePool represents a named pool of replicas within a project
type ServicePool struct {
	Desired  int      `json:"desired"`
	Healthy  int      `json:"healthy"`
	VMs      []string `json:"vms"`
	Replicas []string `json:"replicas,omitempty"`
}

// Healthcheck defines health check configuration
type Healthcheck struct {
	Type        string `json:"type,omitempty"`         // http, tcp, exec
	Path        string `json:"path,omitempty"`         // HTTP path
	Port        int    `json:"port,omitempty"`         // Port to check
	IntervalSec int    `json:"interval_sec,omitempty"` // Check interval in seconds
	TimeoutSec  int    `json:"timeout_sec,omitempty"`  // Timeout in seconds
	Threshold   int    `json:"threshold,omitempty"`    // Success threshold
}

// AutoscalePolicy controls horizontal autoscaling
type AutoscalePolicy struct {
	MinReplicas     int     `json:"min_replicas"`
	MaxReplicas     int     `json:"max_replicas"`
	TargetCPU       float64 `json:"target_cpu_percent"`
	ScaleDownCPU    float64 `json:"scale_down_cpu_percent"`
	CooldownSec     int     `json:"cooldown_seconds"`
	Enabled         bool    `json:"enabled"`
	MetricsInterval int     `json:"metrics_interval_seconds,omitempty"`
}
