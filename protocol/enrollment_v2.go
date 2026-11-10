package protocol

import (
	"fmt"
	"strings"
	"unicode"
)

// AgentEnrollmentRequestV2 is sent only to the HTTPS v2 enrollment endpoint.
// The short-lived enrollment secret is carried in the Authorization header,
// never in this body. Node identity is allocated by the control plane.
type AgentEnrollmentRequestV2 struct {
	ProtocolVersion string `json:"protocol_version"`
	Hostname        string `json:"hostname"`
}

func (r AgentEnrollmentRequestV2) Validate() error {
	if r.ProtocolVersion != VersionV2 {
		return fmt.Errorf("protocol_version must be %q", VersionV2)
	}
	if r.Hostname == "" || strings.TrimSpace(r.Hostname) != r.Hostname || len([]rune(r.Hostname)) > 128 {
		return fmt.Errorf("hostname must contain 1 to 128 characters without surrounding whitespace")
	}
	for _, character := range r.Hostname {
		if unicode.IsControl(character) {
			return fmt.Errorf("hostname must not contain control characters")
		}
	}
	return nil
}

// AgentEnrollmentResponseV2 returns the newly bound server-created node ID and
// its node credential exactly once. The server persists only the credential
// hash; the enrolling agent stores the secret in a protected local token file.
type AgentEnrollmentResponseV2 struct {
	ProtocolVersion      string `json:"protocol_version"`
	NodeID               string `json:"node_id"`
	NodeToken            string `json:"node_token"`
	EnrollmentTarget     string `json:"enrollment_target"`
	ControlPlaneURL      string `json:"control_plane_url"`
	CredentialGeneration int64  `json:"credential_generation"`
	State                string `json:"state"`
}

func (r AgentEnrollmentResponseV2) Validate() error {
	if r.ProtocolVersion != VersionV2 {
		return fmt.Errorf("protocol_version must be %q", VersionV2)
	}
	if r.NodeID == "" || strings.TrimSpace(r.NodeID) != r.NodeID || strings.ContainsAny(r.NodeID, "/\\\x00\r\n\t ") {
		return fmt.Errorf("node_id is invalid")
	}
	if r.EnrollmentTarget != "same_host" && r.EnrollmentTarget != "remote_host" {
		return fmt.Errorf("enrollment_target is invalid")
	}
	if r.ControlPlaneURL == "" || r.CredentialGeneration <= 0 || r.State != "offline" {
		return fmt.Errorf("enrollment response is incomplete")
	}
	return nil
}
