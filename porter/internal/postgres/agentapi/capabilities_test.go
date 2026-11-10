package agentapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
)

func TestFleetCapabilitiesHandlerRequiresAdminBearerAndHTTPS(t *testing.T) {
	repo := &agentRepoStub{}
	server, err := New(repo, "operator-secret")
	if err != nil {
		t.Fatal("create agent API server")
	}
	capabilities := v1.FleetCapabilities{
		JoinTokenIssuance: true, AgentEnrollment: true, NodeCredentialRotation: true,
	}
	handler := server.FleetCapabilitiesHandler(capabilities)
	tlsServer := httptest.NewTLSServer(handler)
	t.Cleanup(tlsServer.Close)

	for _, bearer := range []string{"", "wrong-operator-token"} {
		resp := doAgentTLS(t, tlsServer, http.MethodGet, "/", "", map[string]string{
			"Authorization": "Bearer " + bearer,
		})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || strings.Contains(string(body), "operator-secret") {
			t.Fatal("capability probe did not reject missing or invalid admin auth safely")
		}
	}

	ok := doAgentTLS(t, tlsServer, http.MethodGet, "/", "", map[string]string{"Authorization": "Bearer operator-secret"})
	body, err := io.ReadAll(ok.Body)
	ok.Body.Close()
	if err != nil || ok.StatusCode != http.StatusOK || ok.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated capability probe did not return a no-store success")
	}
	var got map[string]bool
	if err := json.Unmarshal(body, &got); err != nil || len(got) != 3 || !got["join_token_issuance"] || !got["agent_enrollment"] || !got["node_credential_rotation"] {
		t.Fatal("capability probe returned an unexpected DTO")
	}
	var typed v1.FleetCapabilities
	if err := json.Unmarshal(body, &typed); err != nil || typed != capabilities {
		t.Fatal("capability probe DTO did not match mounted-route flags")
	}

	query := doAgentTLS(t, tlsServer, http.MethodGet, "/?token=operator-secret", "", map[string]string{"Authorization": "Bearer operator-secret"})
	query.Body.Close()
	if query.StatusCode != http.StatusBadRequest {
		t.Fatal("capability probe accepted a query token")
	}
	if repo.enrollmentIssueCalls != 0 || repo.issueCalls != 0 {
		t.Fatal("capability probe touched an issue or rotation repository operation")
	}

	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)
	resp, err := plain.Client().Get(plain.URL + "/")
	if err != nil {
		t.Fatal("send plaintext capability probe")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatal("capability probe did not require HTTPS")
	}
}
