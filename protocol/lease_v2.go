package protocol

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const VersionV2 = "v2"

var (
	ErrInvalidLeasedMessage = errors.New("invalid v2 leased-command message")
	ErrInvalidLeaseToken    = errors.New("invalid v2 lease token")
	ErrLeaseExpired         = errors.New("v2 command lease is expired")
	ErrStaleAttempt         = errors.New("stale v2 command attempt")
	ErrCommandIdentity      = errors.New("v2 result identity does not match its claim")
	ErrCommandKind          = errors.New("v2 result command kind does not match its claim")
	ErrLeaseMismatch        = errors.New("v2 result lease token does not match its claim")
	ErrInvalidHeartbeat     = errors.New("invalid v2 agent heartbeat")
)

// AgentHeartbeatV2 is a minimal liveness receipt. Node identity is bound to
// the authenticated request header; this message cannot enroll or mutate node
// state beyond its last-seen timestamp.
type AgentHeartbeatV2 struct {
	ProtocolVersion string    `json:"protocol_version"`
	At              time.Time `json:"at"`
}

func (h AgentHeartbeatV2) ValidateAt(now time.Time) error {
	if h.ProtocolVersion != VersionV2 || now.IsZero() || h.At.IsZero() ||
		h.At.Before(now.Add(-5*time.Minute)) || h.At.After(now.Add(time.Minute)) {
		return ErrInvalidHeartbeat
	}
	return nil
}

// CommandV2 is an explicit lease-fenced command claim. The existing Command
// remains the v1 wire shape and does not gain fields or new validation rules.
type CommandV2 struct {
	ProtocolVersion string          `json:"protocol_version"`
	ID              string          `json:"id"`
	NodeID          string          `json:"node_id"`
	OperationID     string          `json:"operation_id"`
	Attempt         int             `json:"attempt"`
	Kind            string          `json:"kind"`
	LeaseToken      string          `json:"lease_token"`
	LeaseExpiresAt  time.Time       `json:"lease_expires_at"`
	Payload         json.RawMessage `json:"payload"`
	State           string          `json:"state"`
	CreatedAt       time.Time       `json:"created_at"`
}

// CommandResultV2 is the result receipt paired with a v2 claim. It repeats the
// command kind and lease token so the control plane can fence the receipt.
type CommandResultV2 struct {
	ProtocolVersion string `json:"protocol_version"`
	CommandID       string `json:"command_id"`
	OperationID     string `json:"operation_id"`
	NodeID          string `json:"node_id"`
	Attempt         int    `json:"attempt"`
	Kind            string `json:"kind"`
	LeaseToken      string `json:"lease_token"`
	Success         bool   `json:"success"`
	Error           string `json:"error,omitempty"`
	State           string `json:"state"`
	SnapshotArtifact *SnapshotArtifactReceipt `json:"snapshot_artifact,omitempty"`
}

func (c CommandV2) Validate() error { return c.ValidateAt(time.Now().UTC()) }

func (c CommandV2) ValidateAt(now time.Time) error {
	if c.ProtocolVersion != VersionV2 {
		return fmt.Errorf("%w: protocol_version must be %q", ErrInvalidLeasedMessage, VersionV2)
	}
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.NodeID) == "" || strings.TrimSpace(c.OperationID) == "" || c.Attempt < 1 {
		return fmt.Errorf("%w: command, node, operation, and positive attempt are required", ErrInvalidLeasedMessage)
	}
	if !ValidCommandKind(c.Kind) {
		return fmt.Errorf("%w: unsupported command kind", ErrInvalidLeasedMessage)
	}
	if !ValidLeaseToken(c.LeaseToken) {
		return ErrInvalidLeaseToken
	}
	if c.LeaseExpiresAt.IsZero() || !c.LeaseExpiresAt.After(now) {
		return ErrLeaseExpired
	}
	if c.State != "leased" {
		return fmt.Errorf("%w: command state must be leased", ErrInvalidLeasedMessage)
	}
	if len(c.Payload) == 0 || !json.Valid(c.Payload) {
		return fmt.Errorf("%w: command payload must be valid JSON", ErrInvalidLeasedMessage)
	}
	if err := validateSnapshotCommandPayload(c.Kind, c.Payload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLeasedMessage, err)
	}
	return nil
}

func (r CommandResultV2) Validate() error {
	if r.ProtocolVersion != VersionV2 {
		return fmt.Errorf("%w: protocol_version must be %q", ErrInvalidLeasedMessage, VersionV2)
	}
	if strings.TrimSpace(r.CommandID) == "" || strings.TrimSpace(r.OperationID) == "" || strings.TrimSpace(r.NodeID) == "" || r.Attempt < 1 {
		return fmt.Errorf("%w: command, node, operation, and positive attempt are required", ErrInvalidLeasedMessage)
	}
	if !ValidCommandKind(r.Kind) {
		return fmt.Errorf("%w: unsupported command kind", ErrInvalidLeasedMessage)
	}
	if !ValidLeaseToken(r.LeaseToken) {
		return ErrInvalidLeaseToken
	}
	if r.Success {
		if r.Error != "" || !successfulResultStateAllowed(r.Kind, r.State) {
			return fmt.Errorf("%w: success state does not match the typed command", ErrInvalidLeasedMessage)
		}
	} else if r.State != "failed" || strings.TrimSpace(r.Error) == "" {
		return fmt.Errorf("%w: failed result requires state=failed and a non-empty error", ErrInvalidLeasedMessage)
	}
	needsSnapshotReceipt := r.Success && (r.Kind == "snapshot.create" || r.Kind == "snapshot.restore")
	if needsSnapshotReceipt {
		if r.SnapshotArtifact == nil || r.SnapshotArtifact.Validate() != nil {
			return fmt.Errorf("%w: successful snapshot command requires valid artifact metadata", ErrInvalidLeasedMessage)
		}
	} else if r.SnapshotArtifact != nil {
		return fmt.Errorf("%w: artifact metadata is only valid on successful snapshot commands", ErrInvalidLeasedMessage)
	}
	return nil
}

// ValidateFor checks a result against the exact claim that produced it. The
// server remains authoritative for persisted attempt and lease-expiry checks.
func (r CommandResultV2) ValidateFor(command CommandV2) error {
	return r.ValidateForAt(command, time.Now().UTC())
}

func (r CommandResultV2) ValidateForAt(command CommandV2, now time.Time) error {
	if err := command.ValidateAt(now); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.CommandID != command.ID || r.OperationID != command.OperationID || r.NodeID != command.NodeID {
		return ErrCommandIdentity
	}
	if r.Attempt != command.Attempt {
		return ErrStaleAttempt
	}
	if r.Kind != command.Kind {
		return ErrCommandKind
	}
	if subtle.ConstantTimeCompare([]byte(r.LeaseToken), []byte(command.LeaseToken)) != 1 {
		return ErrLeaseMismatch
	}
	if err := validateSnapshotResultForCommand(command, r); err != nil {
		return fmt.Errorf("%w: %v", ErrCommandIdentity, err)
	}
	return nil
}

func ValidCommandKind(kind string) bool {
	switch kind {
	case "vm.apply", "vm.delete", "vm.start", "vm.stop", "vm.restart",
		"snapshot.create", "snapshot.restore", "volume.resize":
		return true
	default:
		return false
	}
}

// ValidLeaseToken matches the 256-bit unpadded base64url tokens generated by
// the PostgreSQL command lease store.
func ValidLeaseToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32
}

func successfulResultStateAllowed(kind, state string) bool {
	switch kind {
	case "vm.apply", "vm.start", "vm.restart":
		return state == "running"
	case "vm.stop":
		return state == "stopped"
	case "vm.delete":
		return state == "deleted"
	case "snapshot.create":
		return state == "snapshot_ready"
	case "snapshot.restore":
		return state == "restored"
	case "volume.resize":
		return state == "resized"
	default:
		return false
	}
}
