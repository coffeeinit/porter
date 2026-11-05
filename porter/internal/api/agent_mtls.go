package api

import (
	"crypto/x509"
	"net/http"
	"net/url"
)

var agentMTLSRoutes = map[string]struct{}{
	"POST /v1/fleet/heartbeat": {},
	"GET /v1/fleet/commands":   {},
	"POST /v1/fleet/result":    {},
	"POST /v1/fleet/logs":      {},
	"POST /v1/fleet/snapshot":  {},
	"POST /v1/fleet/vmstate":   {},
}

// RequireAgentMTLS rejects fleet-agent traffic without a verified client
// certificate whose SPIFFE-style URI identity matches X-Porter-Node-ID.
// Enrollment and admin routes remain available without a client certificate.
func RequireAgentMTLS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, required := agentMTLSRoutes[r.Method+" "+r.URL.Path]; !required {
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
			writeError(w, http.StatusUnauthorized, "verified agent client certificate required")
			return
		}
		nodeID := r.Header.Get("X-Porter-Node-ID")
		if nodeID == "" || !agentCertificateMatchesNode(r.TLS.PeerCertificates[0], nodeID) {
			writeError(w, http.StatusUnauthorized, "agent client certificate does not match node id")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func agentCertificateMatchesNode(cert *x509.Certificate, nodeID string) bool {
	if cert == nil || nodeID == "" {
		return false
	}
	want := "spiffe://porter/node/" + url.PathEscape(nodeID)
	for _, uri := range cert.URIs {
		if uri.String() == want {
			return true
		}
	}
	return false
}
