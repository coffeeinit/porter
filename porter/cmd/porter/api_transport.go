package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// validateAPIServerTransport prevents admin and node bearer tokens from crossing
// a plaintext network listener. HTTP is limited to explicit loopback development.
func validateAPIServerTransport(addr string, tlsEnabled bool, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = apiModeProduction
	}
	if mode != apiModeDevelopment && mode != apiModeProduction {
		return fmt.Errorf("PORTER_API_MODE must be %q or %q", apiModeProduction, apiModeDevelopment)
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("PORTER_ADDR must be a host:port listener address")
	}
	if mode == apiModeProduction && !tlsEnabled {
		return fmt.Errorf("API TLS is required in production; configure PORTER_API_TLS_CERT and PORTER_API_TLS_KEY (or explicitly use development mode on loopback)")
	}
	if tlsEnabled {
		return nil
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if !loopback {
		return fmt.Errorf("plaintext API listeners must bind to loopback; use PORTER_ADDR=127.0.0.1:PORT or [::1]:PORT for local development, or configure API TLS")
	}
	return nil
}

func validatePostgresOnlyGatewaySetting(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "off") {
		return nil
	}
	return fmt.Errorf("PORTER_GATEWAY_ADDR is unavailable in the PostgreSQL-only beta; gateway routing is deferred")
}

// requireAgentClientCertificate adds node-bound mTLS when a client CA is
// configured. Initial enrollment and operator endpoints stay bearer-authenticated
// over server-verified HTTPS so a new node can obtain its first identity.
func requireAgentClientCertificate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/agent/") || r.URL.Path == "/internal/agent/enroll" {
			next.ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "verified agent client certificate required", http.StatusUnauthorized)
			return
		}
		nodeID := r.Header.Get("X-Porter-Node-ID")
		if nodeID == "" || strings.TrimSpace(nodeID) != nodeID || strings.ContainsAny(nodeID, "/\\\x00\r\n\t ") {
			http.Error(w, "valid agent node identity required", http.StatusUnauthorized)
			return
		}
		wantIdentity := "spiffe://porter/node/" + nodeID
		for _, identity := range r.TLS.PeerCertificates[0].URIs {
			if identity.String() == wantIdentity {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "agent certificate identity does not match node identity", http.StatusUnauthorized)
	})
}
