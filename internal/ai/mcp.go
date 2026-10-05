// MCP facade model (OCM-14 remainder): workspace-scoped connectors exposed
// through one streamable-HTTP server as search → describe → call. Provider
// credentials resolve server-side at call time and never enter the workload.
// Calls are audited as events; destructive tools need explicit approval.
package ai

import (
	"fmt"
	"strings"
)

// Call policies.
const (
	PolicyAllow           = "allow"
	PolicyRequireApproval = "require_approval"
)

// Connector is one workspace integration (GitHub, Google, OpenAPI…).
type Connector struct {
	Address  string // wi.<workspace>.<kind>.<name>, e.g. wi.acme.github.main
	Policy   string // allow | require_approval
	degraded bool   // reserved for degraded-mode wiring
}

// ParseAddress validates a tool address and splits its coordinates.
func ParseAddress(addr string) (workspace, kind, name string, err error) {
	parts := strings.Split(addr, ".")
	if len(parts) != 4 || parts[0] != "wi" {
		return "", "", "", fmt.Errorf("ai: bad tool address %q (want wi.<workspace>.<kind>.<name>)", addr)
	}
	for _, p := range parts[1:] {
		if p == "" {
			return "", "", "", fmt.Errorf("ai: bad tool address %q", addr)
		}
	}
	return parts[1], parts[2], parts[3], nil
}

// Validate gates connector registration.
func (c Connector) Validate() error {
	if _, _, _, err := ParseAddress(c.Address); err != nil {
		return err
	}
	switch c.Policy {
	case PolicyAllow, PolicyRequireApproval:
	default:
		return fmt.Errorf("ai: unknown call policy %q", c.Policy)
	}
	return nil
}

// Call is one audited tool invocation.
type Call struct {
	Address    string
	Args       map[string]string
	Approved   bool
	ApprovedBy string
}

// Authorize enforces the connector policy before execution.
func (c Connector) Authorize(call Call) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if call.Address != c.Address {
		return fmt.Errorf("ai: call address mismatch")
	}
	if c.Policy == PolicyRequireApproval && !call.Approved {
		return fmt.Errorf("ai: %s needs explicit approval", c.Address)
	}
	return nil
}
