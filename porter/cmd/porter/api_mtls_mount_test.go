package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresAgentClientCertificateGateIsNodeBound(t *testing.T) {
	files := writeAPITestCertificates(t)
	tlsConfig, requireAgentMTLS, err := loadAPITLSConfig(files.serverCert, files.serverKey, files.caCert)
	if err != nil || !requireAgentMTLS {
		t.Fatalf("mTLS config=(%v,%t,%v)", tlsConfig, requireAgentMTLS, err)
	}
	server := httptest.NewUnstartedServer(requireAgentClientCertificate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()

	noCertClient := testTLSClient(t, files.caCert, "", "")
	assertAPIStatus(t, noCertClient, server.URL+"/internal/agent/heartbeat", "node-1", http.StatusUnauthorized)
	assertAPIStatus(t, noCertClient, server.URL+"/internal/agent/enroll", "", http.StatusNoContent)
	assertAPIStatus(t, noCertClient, server.URL+"/internal/admin/fleet/join-tokens", "", http.StatusNoContent)

	validClient := testTLSClient(t, files.caCert, files.node1Cert, files.node1Key)
	assertAPIStatus(t, validClient, server.URL+"/internal/agent/heartbeat", "node-1", http.StatusNoContent)
	assertAPIStatus(t, validClient, server.URL+"/internal/agent/heartbeat", "node-2", http.StatusUnauthorized)

	wrongCAClient := testTLSClient(t, files.caCert, files.node3Cert, files.node3Key)
	assertAPIAccessDenied(t, wrongCAClient, server.URL+"/internal/agent/heartbeat", "node-1")
}
