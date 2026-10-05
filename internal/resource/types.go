// Package resource defines the canonical Porter domain types shared across
// the API, store, controllers, runtime, and dashboard.
//
// This package is the single source of truth for the Porter resource model.
// All other packages import types from here.
package resource

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Constants for resource states
const (
	// VM/Workload states
	StatePending  = "pending"
	StateBooting  = "booting"
	StateRunning  = "running"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateFailed   = "failed"
	StateDeleting = "deleting"
	StateDeleted  = "deleted"

	// Health states
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthChecking  = "checking"
	HealthUnknown   = "unknown"
	HealthDegraded  = "degraded"

	// Condition types
	ConditionReady    = "Ready"
	ConditionHealthy  = "Healthy"
	ConditionSynced   = "Synced"
	ConditionProgress = "Progress"

	// Condition statuses
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"

	// Generic phase/status values used by controllers for both the node
	// lifecycle and build reconciliation.
	StatusBuilding  = "building"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusReady     = "ready"

	// Resource kinds
	KindProject        = "Project"
	KindEnvironment    = "Environment"
	KindService        = "Service"
	KindDeployment     = "Deployment"
	KindReplica        = "Replica"
	KindVolume         = "Volume"
	KindNetwork        = "Network"
	KindDomain         = "Domain"
	KindCertificate    = "Certificate"
	KindSecret         = "Secret"
	KindBuild          = "Build"
	KindImage          = "Image"
	KindNode           = "Node"
	KindCluster        = "Cluster"
	KindUser           = "User"
	KindOrg            = "Org"
	KindTeam           = "Team"
	KindRole           = "Role"
	KindPermission     = "Permission"
	KindRoleAssignment = "RoleAssignment"
	KindReseller       = "Reseller"
	KindMicroVM        = "MicroVM"
	KindIPAllocation   = "IPAllocation"
	KindAPIKey         = "APIKey"
	KindHook           = "Hook"
	KindCron           = "Cron"
	KindAlert          = "Alert"
	KindIncident       = "Incident"
	KindWorkflow       = "Workflow"
	KindTask           = "Task"
	KindEvent          = "Event"
	KindMetric         = "Metric"
	KindLog            = "Log"
	KindTrace          = "Trace"
)

// Common metadata embedded in all resources
type Metadata struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"` // org/project scope
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Version     int64             `json:"version,omitempty"` // for optimistic locking
}

// ObjectRef is a reference to another resource
type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// OwnerReference for garbage collection and lifecycle
type OwnerReference struct {
	APIVersion         string `json:"api_version"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	UID                string `json:"uid"`
	Controller         *bool  `json:"controller,omitempty"`
	BlockOwnerDeletion *bool  `json:"block_owner_deletion,omitempty"`
}

// Condition represents a resource condition
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"` // True, False, Unknown
	LastTransitionTime time.Time `json:"last_transition_time"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	ObservedGeneration int64     `json:"observed_generation,omitempty"`
}

// Status is the observed state of a resource
type Status struct {
	Conditions         []Condition `json:"conditions,omitempty"`
	Phase              string      `json:"phase,omitempty"`
	Message            string      `json:"message,omitempty"`
	ObservedGeneration int64       `json:"observed_generation,omitempty"`
	Replicas           int         `json:"replicas,omitempty"`
	ReadyReplicas      int         `json:"ready_replicas,omitempty"`
}

// Resource is the base interface all Porter resources implement
type Resource interface {
	GetMetadata() *Metadata
	GetSpec() interface{}
	GetStatus() *Status
	SetStatus(*Status)
	GetKind() string
}

// NewMetadata creates metadata with generated ID
func NewMetadata(name, namespace string) *Metadata {
	now := time.Now()
	return &Metadata{
		ID:          uuid.New().String(),
		Name:        name,
		Namespace:   namespace,
		Labels:      make(map[string]string),
		Annotations: make(map[string]string),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// MarshalJSON custom marshaling for conditions
func (c Condition) MarshalJSON() ([]byte, error) {
	type Alias Condition
	return json.Marshal(struct {
		Alias
		LastTransitionTime string `json:"last_transition_time"`
	}{
		Alias:              Alias(c),
		LastTransitionTime: c.LastTransitionTime.Format(time.RFC3339),
	})
}

// UnmarshalJSON custom unmarshaling for conditions
func (c *Condition) UnmarshalJSON(data []byte) error {
	type Alias Condition
	aux := struct {
		Alias
		LastTransitionTime string `json:"last_transition_time"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*c = Condition(aux.Alias)
	if aux.LastTransitionTime != "" {
		t, err := time.Parse(time.RFC3339, aux.LastTransitionTime)
		if err != nil {
			return err
		}
		c.LastTransitionTime = t
	}
	return nil
}
