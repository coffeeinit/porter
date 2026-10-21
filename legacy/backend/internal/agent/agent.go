// Host-agent identity, enrollment, heartbeat and lifecycle helpers
// (OCM-01/05/06/07). The agent process itself lives outside this package;
// here are the shared types both control plane and agent build against:
// dual-port config (:9090 control Bearer vs :9091 loopback proxy),
// one-time enrollment tokens, heartbeat payloads, transient unit naming,
// and the manual rollout version check.
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ControlPort is the firewalled Bearer control API. ProxyPort is the
// loopback-only data proxy (X-Proxy-Token). Never swap them (OCM-07).
const (
	ControlPort = 9090
	ProxyPort   = 9091
)

// RuntimeOwner selects how the agent supervises Firecracker (OCM-01).
// systemd-unit survives agent restarts; direct is for dev/test.
type RuntimeOwner string

const (
	OwnerSystemdUnit RuntimeOwner = "systemd-unit"
	OwnerDirect      RuntimeOwner = "direct"
)

// ParseOwner validates the VM_RUNTIME_OWNER style value.
func ParseOwner(s string) (RuntimeOwner, error) {
	switch RuntimeOwner(s) {
	case OwnerSystemdUnit, OwnerDirect:
		return RuntimeOwner(s), nil
	case "":
		return OwnerSystemdUnit, nil
	default:
		return "", fmt.Errorf("agent: unknown runtime owner %q", s)
	}
}

// UnitName returns the transient systemd unit owning one VM
// (porter-vm-<id>.service, KillMode=process so agent restarts are safe).
func UnitName(vmID string) string {
	return "porter-vm-" + sanitize(vmID) + ".service"
}

func sanitize(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() == 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// EnrollmentToken is a one-time, expiring host bootstrap credential (OCM-05).
// Identity is cryptographic (random 256-bit), never IP/HW derived.
type EnrollmentToken struct {
	Token     string
	Provider  string // hetzner | ovhcloud | gcp | customer_owned
	Labels    map[string]string
	ExpiresAt time.Time
	UsedBy    string // node id once consumed, "" = unused
}

// NewEnrollmentToken mints a token valid for ttl.
func NewEnrollmentToken(provider string, ttl time.Duration) (EnrollmentToken, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return EnrollmentToken{}, fmt.Errorf("agent: mint token: %w", err)
	}
	return EnrollmentToken{
		Token:     "penr_" + hex.EncodeToString(raw[:]),
		Provider:  provider,
		ExpiresAt: time.Now().Add(ttl),
	}, nil
}

// ValidateToken rejects malformed tokens before any DB lookup.
func ValidateToken(tok string) error {
	if !strings.HasPrefix(tok, "penr_") || len(tok) != 5+64 {
		return fmt.Errorf("agent: malformed enrollment token")
	}
	if _, err := hex.DecodeString(tok[5:]); err != nil {
		return fmt.Errorf("agent: malformed enrollment token: %w", err)
	}
	return nil
}

// Consumable reports whether the token can still enroll a node.
func (t EnrollmentToken) Consumable(now time.Time) error {
	if t.UsedBy != "" {
		return fmt.Errorf("agent: enrollment token already used by %s", t.UsedBy)
	}
	if !now.Before(t.ExpiresAt) {
		return fmt.Errorf("agent: enrollment token expired")
	}
	return nil
}

// Heartbeat is the agent liveness + capacity report (OCM-06). The
// reconciler derives connectivity (online|stale|offline) from At alone;
// providers add existence checks on top, never inside this struct.
type Heartbeat struct {
	NodeID       string
	AgentVersion string
	VCPUFree     int
	MemFreeMiB   int
	VMCount      int
	Capabilities map[string]bool // kvm | jailer | virtiofs | browser_storage ...
	At           time.Time
}

// StaleAfter classifies heartbeat age for the reconciler tick.
func (h Heartbeat) StaleAfter(now time.Time, staleAfter time.Duration) bool {
	return now.Sub(h.At) > staleAfter
}

// ShouldUpdate reports version drift for the manual rollout badge (OCM-12):
// empty actual always wants the desired version; equal versions do not.
func ShouldUpdate(actual, desired string) bool {
	if desired == "" || actual == desired {
		return false
	}
	return true
}
