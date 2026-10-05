// Package resource defines the canonical Porter domain types.
package resource

import (
	"time"
)

// Domain is a custom domain attached to a project.
type Domain struct {
	Metadata `json:"metadata"`
	Spec     DomainSpec   `json:"spec"`
	Status   DomainStatus `json:"status"`
}

func (d *Domain) GetMetadata() *Metadata { return &d.Metadata }
func (d *Domain) GetSpec() interface{}   { return d.Spec }
func (d *Domain) GetStatus() *Status     { return &d.Status.Status }
func (d *Domain) SetStatus(s *Status)    { d.Status.Status = *s }
func (d *Domain) GetKind() string        { return KindDomain }

// DomainSpec is the desired state of a domain
type DomainSpec struct {
	// ProjectID is the owning project
	ProjectID string `json:"project_id"`

	// Domain is the domain name
	Domain string `json:"domain"`

	// Type is the domain type (custom, preview, wildcard)
	Type string `json:"type"`

	// DNSProvider is the DNS provider (porter, cloudflare, route53, etc.)
	DNSProvider string `json:"dns_provider,omitempty"`

	// DNSZone is the DNS zone ID
	DNSZone string `json:"dns_zone,omitempty"`

	// CertificateID is the certificate to use
	CertificateID string `json:"certificate_id,omitempty"`

	// Wildcard indicates if this is a wildcard domain
	Wildcard bool `json:"wildcard,omitempty"`

	// AutoTLS enables automatic TLS via ACME
	AutoTLS bool `json:"auto_tls,omitempty"`

	// TLSEmail is the email for ACME registration
	TLSEmail string `json:"tls_email,omitempty"`

	// PathRoutes for path-based routing
	PathRoutes []PathRoute `json:"path_routes,omitempty"`
}

// PathRoute defines a path-based route
type PathRoute struct {
	Path        string `json:"path"`
	ServiceName string `json:"service_name"`
	Port        int    `json:"port"`
}

// DomainStatus is the observed state of a domain
type DomainStatus struct {
	Status `json:",inline"`

	// Phase is the current phase
	Phase string `json:"phase"`

	// Verified indicates if domain ownership is verified
	Verified bool `json:"verified"`

	// DNSRecords is the list of DNS records
	DNSRecords []DNSRecord `json:"dns_records,omitempty"`

	// Certificate is the certificate info
	Certificate *CertificateRef `json:"certificate,omitempty"`

	// ExpiresAt is the domain expiration
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// VerificationToken is the token for domain verification
	VerificationToken string `json:"verification_token,omitempty"`
}

// DNSRecord is a DNS record
type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

// CertificateRef is a reference to a certificate
type CertificateRef struct {
	ID        string     `json:"id"`
	Subject   string     `json:"subject"`
	Issuer    string     `json:"issuer"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Status    string     `json:"status"`
}
