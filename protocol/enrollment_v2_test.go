package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentEnrollmentV2ValidatesVersionAndServerAssignedIdentity(t *testing.T) {
	valid := AgentEnrollmentRequestV2{ProtocolVersion: VersionV2, Hostname: "porter-agent-01"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid enrollment request: %v", err)
	}
	for _, request := range []AgentEnrollmentRequestV2{
		{},
		{ProtocolVersion: Version, Hostname: "porter-agent-01"},
		{ProtocolVersion: VersionV2},
		{ProtocolVersion: VersionV2, Hostname: " agent-01"},
		{ProtocolVersion: VersionV2, Hostname: "agent\n01"},
		{ProtocolVersion: VersionV2, Hostname: strings.Repeat("a", 129)},
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("invalid request %+v was accepted", request)
		}
	}
	validResponse := AgentEnrollmentResponseV2{
		ProtocolVersion: VersionV2, NodeID: "node_issued", NodeToken: strings.Repeat("x", 43),
		EnrollmentTarget: "remote_host", ControlPlaneURL: "https://control.example", CredentialGeneration: 1, State: "offline",
	}
	if err := validResponse.Validate(); err != nil {
		t.Fatalf("valid response: %v", err)
	}
	invalidResponse := validResponse
	invalidResponse.EnrollmentTarget = "other_host"
	if err := invalidResponse.Validate(); err == nil {
		t.Fatal("unsupported enrollment target was accepted")
	}
}

func TestV2EnrollmentWireIsAdditiveAndLegacyV1EnrollmentRemainsUnversioned(t *testing.T) {
	legacy := EnrollRequest{Token: "legacy-token", Hostname: "legacy-host", OS: "linux", Arch: "amd64", CPUs: 4, MemoryMB: 4096, DiskMB: 20480}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyFields map[string]json.RawMessage
	if err := json.Unmarshal(legacyJSON, &legacyFields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"token", "hostname", "os", "arch", "cpus", "memory_mb", "disk_mb"} {
		if _, ok := legacyFields[key]; !ok {
			t.Errorf("legacy v1 enrollment JSON lost field %q: %s", key, legacyJSON)
		}
	}
	if _, ok := legacyFields["protocol_version"]; ok {
		t.Fatalf("legacy v1 enrollment was silently versioned: %s", legacyJSON)
	}

	v2JSON, err := json.Marshal(AgentEnrollmentRequestV2{ProtocolVersion: VersionV2, Hostname: "v2-host"})
	if err != nil {
		t.Fatal(err)
	}
	var v2Fields map[string]json.RawMessage
	if err := json.Unmarshal(v2JSON, &v2Fields); err != nil {
		t.Fatal(err)
	}
	if len(v2Fields) != 2 || string(v2Fields["protocol_version"]) != `"v2"` || string(v2Fields["hostname"]) != `"v2-host"` {
		t.Fatalf("v2 enrollment JSON=%s", v2JSON)
	}
}
