// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Secret is an encrypted per-project secret.
type Secret struct {
	Metadata `json:"metadata"`
	Spec     SecretSpec   `json:"spec"`
	Status   SecretStatus `json:"status"`
}

func (s *Secret) GetMetadata() *Metadata { return &s.Metadata }
func (s *Secret) GetSpec() interface{}   { return s.Spec }
func (s *Secret) GetStatus() *Status     { return &s.Status.Status }
func (s *Secret) SetStatus(st *Status)   { s.Status.Status = *st }
func (s *Secret) GetKind() string        { return KindSecret }

// SecretSpec is the desired state of a secret
type SecretSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id"`

	// Name is the secret name
	Name string `json:"name"`

	// Value is the secret value (plaintext input, encrypted at rest)
	Value string `json:"value,omitempty"`

	// ValueEncrypted is the encrypted value (internal use)
	ValueEncrypted []byte `json:"-"`

	// Scope is the secret scope (project, environment, service)
	Scope string `json:"scope,omitempty"`

	// Type is the secret type (opaque, tls, dockerconfig, etc.)
	Type string `json:"type,omitempty"`

	// Immutable prevents updates
	Immutable bool `json:"immutable,omitempty"`
}

// SecretStatus is the observed state of a secret
type SecretStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"`

	// LastRotatedAt is when the secret was last rotated
	LastRotatedAt *time.Time `json:"last_rotated_at,omitempty"`

	// RotationPolicy is the rotation policy
	RotationPolicy string `json:"rotation_policy,omitempty"`

	// Checksum is the value checksum for verification
	Checksum string `json:"checksum,omitempty"`
}
