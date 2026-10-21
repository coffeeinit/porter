// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// APIKey is a long-lived token for programmatic access.
type APIKey struct {
	Metadata `json:"metadata"`
	Spec     APIKeySpec   `json:"spec"`
	Status   APIKeyStatus `json:"status"`
}

func (a *APIKey) GetMetadata() *Metadata { return &a.Metadata }
func (a *APIKey) GetSpec() interface{}   { return a.Spec }
func (a *APIKey) GetStatus() *Status     { return &a.Status.Status }
func (a *APIKey) SetStatus(s *Status)    { a.Status.Status = *s }
func (a *APIKey) GetKind() string        { return KindAPIKey }

// APIKeySpec is the desired state of an API key
type APIKeySpec struct {
	// UserID is the owning user
	UserID string `json:"user_id"`

	// Name is the key name
	Name string `json:"name"`

	// TokenHash is the hashed token (internal)
	TokenHash string `json:"-"`

	// Scopes are the permission scopes
	Scopes []string `json:"scopes,omitempty"`

	// ExpiresAt is the expiration time
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Active indicates if the key is active
	Active bool `json:"active,omitempty"`
}

// APIKeyStatus is the observed state of an API key
type APIKeyStatus struct {
	Status `json:",inline"`

	// LastUsedAt is the last use time
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	// UseCount is the number of uses
	UseCount int64 `json:"use_count,omitempty"`
}
