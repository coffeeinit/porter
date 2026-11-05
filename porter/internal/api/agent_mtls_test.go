package api

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRequireAgentMTLSProtectsFleetRoutes(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/fleet/heartbeat"},
		{http.MethodGet, "/v1/fleet/commands"},
		{http.MethodPost, "/v1/fleet/result"},
		{http.MethodPost, "/v1/fleet/logs"},
		{http.MethodPost, "/v1/fleet/snapshot"},
		{http.MethodPost, "/v1/fleet/vmstate"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			nextCalled := false
			handler := RequireAgentMTLS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(route.method, route.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || nextCalled {
				t.Fatalf("without TLS: status=%d nextCalled=%t, want 401 and no handler", response.Code, nextCalled)
			}

			request = httptest.NewRequest(route.method, route.path, nil)
			request.Header.Set("X-Porter-Node-ID", "node-1")
			request.TLS = verifiedTestClientState("spiffe://porter/node/node-1")
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || !nextCalled {
				t.Fatalf("matching verified cert: status=%d nextCalled=%t, want 204 and handler", response.Code, nextCalled)
			}
		})
	}
}

func TestRequireAgentMTLSRejectsUnverifiedOrMismatchedCertificate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *tls.ConnectionState
	}{
		{name: "unverified chain", state: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}},
		{name: "wrong node identity", state: verifiedTestClientState("spiffe://porter/node/node-2")},
		{name: "no URI identity", state: verifiedTestClientState("spiffe://porter/agent/node-1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := RequireAgentMTLS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Fatal("handler called for an untrusted or mismatched client")
			}))
			request := httptest.NewRequest(http.MethodPost, "/v1/fleet/heartbeat", nil)
			request.Header.Set("X-Porter-Node-ID", "node-1")
			request.TLS = tc.state
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", response.Code)
			}
		})
	}
}

func TestRequireAgentMTLSLeavesEnrollmentAndAdminRoutesAvailable(t *testing.T) {
	handler := RequireAgentMTLS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/fleet/enroll"},
		{http.MethodPost, "/v1/fleet/join-tokens"},
		{http.MethodGet, "/v1/fleet/nodes/node-1"},
		{http.MethodGet, "/healthz"},
	} {
		t.Run(route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status=%d, want 204", response.Code)
			}
		})
	}
}

func verifiedTestClientState(identity string) *tls.ConnectionState {
	uri, _ := url.Parse(identity)
	cert := &x509.Certificate{URIs: []*url.URL{uri}}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}
