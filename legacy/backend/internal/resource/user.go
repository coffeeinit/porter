// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// User is a platform user account.
type User struct {
	Metadata `json:"metadata"`
	Spec     UserSpec   `json:"spec"`
	Status   UserStatus `json:"status"`
}

func (u *User) GetMetadata() *Metadata { return &u.Metadata }
func (u *User) GetSpec() interface{}   { return u.Spec }
func (u *User) GetStatus() *Status     { return &u.Status.Status }
func (u *User) SetStatus(s *Status)    { u.Status.Status = *s }
func (u *User) GetKind() string        { return KindUser }

// UserSpec is the desired state of a user
type UserSpec struct {
	// Username is the unique username
	Username string `json:"username"`

	// Email is the user email
	Email string `json:"email,omitempty"`

	// Role is the user role
	Role string `json:"role,omitempty"`

	// PasswordHash is the hashed password (internal)
	PasswordHash string `json:"-"`

	// Salt is the password salt (internal)
	Salt string `json:"-"`

	// NotifyOptIn for notifications
	NotifyOptIn bool `json:"notify_opt_in,omitempty"`

	// Active indicates if the user is active
	Active bool `json:"active,omitempty"`
}

// UserStatus is the observed state of a user
type UserStatus struct {
	Status `json:",inline"`

	// LastLoginAt is the last login time
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`

	// APIKeyCount is the number of API keys
	APIKeyCount int `json:"api_key_count,omitempty"`
}
