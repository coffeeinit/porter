// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Certificate is a TLS certificate managed by Porter.
type Certificate struct {
	Metadata `json:"metadata"`
	Spec     CertificateSpec   `json:"spec"`
	Status   CertificateStatus `json:"status"`
}

func (c *Certificate) GetMetadata() *Metadata { return &c.Metadata }
func (c *Certificate) GetSpec() interface{}   { return c.Spec }
func (c *Certificate) GetStatus() *Status     { return &c.Status.Status }
func (c *Certificate) SetStatus(s *Status)    { c.Status.Status = *s }
func (c *Certificate) GetKind() string        { return KindCertificate }

// CertificateSpec is the desired state of a certificate
type CertificateSpec struct {
	// Domain is the domain name
	Domain string `json:"domain"`

	// DNSNames are the SANs
	DNSNames []string `json:"dns_names,omitempty"`

	// Issuer is the certificate issuer (letsencrypt, selfsigned, external)
	Issuer string `json:"issuer"`

	// ACME configuration
	ACME *ACMEConfig `json:"acme,omitempty"`

	// PrivateKey is the private key (for external certs)
	PrivateKey string `json:"private_key,omitempty"`

	// Certificate is the certificate PEM (for external certs)
	Certificate string `json:"certificate,omitempty"`

	// AutoRenew enables automatic renewal
	AutoRenew bool `json:"auto_renew,omitempty"`

	// RenewBeforeDays renew before expiry
	RenewBeforeDays int `json:"renew_before_days,omitempty"`
}

// ACMEConfig for Let's Encrypt
type ACMEConfig struct {
	Email       string `json:"email"`
	Server      string `json:"server,omitempty"`       // ACME server URL
	Challenge   string `json:"challenge,omitempty"`    // http-01, dns-01
	DNSProvider string `json:"dns_provider,omitempty"` // for dns-01
}

// CertificateStatus is the observed state of a certificate
type CertificateStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"`

	// ExpiresAt is the certificate expiration
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// IssuedAt is when the certificate was issued
	IssuedAt *time.Time `json:"issued_at,omitempty"`

	// RenewalStatus is the renewal status
	RenewalStatus string `json:"renewal_status,omitempty"`

	// LastRenewalAt is the last renewal attempt
	LastRenewalAt *time.Time `json:"last_renewal_at,omitempty"`

	// PrivateKeyRef is a reference to the stored private key
	PrivateKeyRef string `json:"private_key_ref,omitempty"`

	// CertificateRef is a reference to the stored certificate
	CertificateRef string `json:"certificate_ref,omitempty"`
}
