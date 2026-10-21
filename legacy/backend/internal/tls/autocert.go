// Package tls provides automatic TLS certificate management using Let's Encrypt
// via the ACME protocol. It uses golang.org/x/crypto/acme/autocert with a
// persistent on-disk cache (autocert.DirCache) so certificates survive
// restarts and renew automatically before expiry.
//
// The Manager handles:
//   - Automatic certificate requests for *.baseDomain
//   - On-disk certificate caching + automatic renewal
//   - HTTP-01 challenge responses (wrap the control plane handler)
package tls

import (
	"crypto/tls"
	"net/http"
	"strings"

	"golang.org/x/crypto/acme/autocert"
)

// Manager handles automatic TLS certificate management.
type Manager struct {
	baseDomain string
	autocert   *autocert.Manager
}

// NewManager creates a TLS manager for baseDomain (and baseDomain itself),
// persisting certificates under cacheDir.
func NewManager(baseDomain, email, cacheDir string) *Manager {
	if cacheDir == "" {
		cacheDir = "certs"
	}
	base := strings.ToLower(strings.TrimSuffix(baseDomain, "."))
	return &Manager{
		baseDomain: base,
		autocert: &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Email:      email,
			HostPolicy: autocert.HostWhitelist("*."+base, base),
			Cache:      autocert.DirCache(cacheDir),
		},
	}
}

// GetCertificate returns a TLS certificate for the client's server name,
// provisioning (or renewing) from Let's Encrypt as needed.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil || hello.ServerName == "" {
		return nil, nil
	}
	return m.autocert.GetCertificate(hello)
}

// GetTLSConfig returns a TLS config for the control-plane server.
func (m *Manager) GetTLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: m.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}

// HTTPHandler returns an HTTP handler that serves ACME HTTP-01 challenges and
// delegates everything else to next.
func (m *Manager) HTTPHandler(next http.Handler) http.Handler {
	return m.autocert.HTTPHandler(next)
}
