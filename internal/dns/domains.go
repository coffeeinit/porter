// Package dns — domain auto-assignment and preview/production domain management.
//
// When a project is created, Porter automatically assigns:
//   - Preview domain: <project-slug>.preview.<baseDomain>
//   - Production domain: <project-slug>.<baseDomain>
//
// These domains are stored in the project's domain list and can be verified
// via DNS lookup.
package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strings"

	"porter/internal/store"
	"porter/internal/types"
)

// DomainManager handles automatic domain assignment for projects.
type DomainManager struct {
	store      *store.Store
	baseDomain string
	gatewayIP  string // IP to point A records at
}

// NewDomainManager creates a domain manager with the given base domain.
func NewDomainManager(s *store.Store, baseDomain, gatewayIP string) *DomainManager {
	return &DomainManager{
		store:      s,
		baseDomain: strings.ToLower(strings.TrimSuffix(baseDomain, ".")),
		gatewayIP:  gatewayIP,
	}
}

// AssignDomains creates preview and production domains for a project.
// Returns the created domains.
func (dm *DomainManager) AssignDomains(project *types.Project) ([]*types.Domain, error) {
	if dm.baseDomain == "" {
		return nil, fmt.Errorf("dns: base_domain not configured")
	}

	slug := projectSlug(project)
	var domains []*types.Domain

	// Preview domain: <slug>.preview.<baseDomain>
	previewDomain := fmt.Sprintf("%s.preview.%s", slug, dm.baseDomain)
	preview := &types.Domain{
		ProjectID: project.ID,
		Domain:    previewDomain,
		Type:      "preview",
		Status:    "pending",
	}
	dm.store.AddDomain(project.ID, preview)
	domains = append(domains, preview)

	// Production domain: <slug>.<baseDomain>
	prodDomain := fmt.Sprintf("%s.%s", slug, dm.baseDomain)
	prod := &types.Domain{
		ProjectID: project.ID,
		Domain:    prodDomain,
		Type:      "production",
		Status:    "pending",
	}
	dm.store.AddDomain(project.ID, prod)
	domains = append(domains, prod)

	// Auto-verify domains that point to our gateway
	dm.verifyDomain(project.ID, preview)
	dm.verifyDomain(project.ID, prod)

	return domains, nil
}

// GetPreviewDomain returns the preview domain for a project.
func (dm *DomainManager) GetPreviewDomain(project *types.Project) string {
	slug := projectSlug(project)
	return fmt.Sprintf("%s.preview.%s", slug, dm.baseDomain)
}

// GetProductionDomain returns the production domain for a project.
func (dm *DomainManager) GetProductionDomain(project *types.Project) string {
	slug := projectSlug(project)
	return fmt.Sprintf("%s.%s", slug, dm.baseDomain)
}

// VerifyDomain checks if a domain resolves to the expected gateway IP.
func (dm *DomainManager) VerifyDomain(domain *types.Domain) (bool, string) {
	resolver := &net.Resolver{}
	ips, err := resolver.LookupHost(context.Background(), domain.Domain)
	if err != nil {
		return false, fmt.Sprintf("DNS lookup failed: %v", err)
	}
	if len(ips) == 0 {
		return false, "no A/AAAA records"
	}

	// Check if any IP matches our gateway
	for _, ip := range ips {
		if ip == dm.gatewayIP {
			return true, fmt.Sprintf("resolves to %s (verified)", ip)
		}
	}

	return false, fmt.Sprintf("resolves to %s (not gateway %s)", strings.Join(ips, ", "), dm.gatewayIP)
}

// verifyDomain removes unverified domains if they don't point to our gateway.
// Since store doesn't have UpdateDomain, we just log the verification status.
func (dm *DomainManager) verifyDomain(projectID string, domain *types.Domain) {
	ok, detail := dm.VerifyDomain(domain)
	if ok {
		domain.Status = "verified"
		log.Printf("dns: domain %s verified: %s", domain.Domain, detail)
	} else {
		domain.Status = "unverified"
		log.Printf("dns: domain %s unverified: %s", domain.Domain, detail)
	}
}

// ChallengeHost is the TXT name proving domain control
// (_porter-challenge.<domain>), DNS-01 style. With a Cloudflare user token
// Porter writes it via UpsertTXT; otherwise the operator copies the value
// the challenge endpoint returns.
func ChallengeHost(domain string) string {
	return "_porter-challenge." + strings.ToLower(strings.TrimSpace(domain))
}

// MintChallengeToken returns a fresh 128-bit hex challenge value.
func MintChallengeToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("dns: mint challenge: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// VerifyTXTChallenge checks the challenge TXT (exact match, any record).
func VerifyTXTChallenge(domain, token string) (bool, string) {
	if token == "" {
		return false, "empty challenge token"
	}
	recs, err := net.DefaultResolver.LookupTXT(context.Background(), ChallengeHost(domain))
	if err != nil {
		return false, fmt.Sprintf("TXT lookup failed: %v", err)
	}
	for _, r := range recs {
		if strings.TrimSpace(r) == token {
			return true, "challenge TXT matches (verified)"
		}
	}
	return false, fmt.Sprintf("%d TXT record(s), none match", len(recs))
}

// projectSlug generates a URL-safe slug from a project.
func projectSlug(p *types.Project) string {
	slug := strings.ToLower(p.Name)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	// Remove non-alphanumeric characters except hyphens
	var b strings.Builder
	for _, c := range slug {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		}
	}
	slug = b.String()
	// Collapse multiple hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "app"
	}
	return slug
}
