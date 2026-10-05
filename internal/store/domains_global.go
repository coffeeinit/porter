// Org-wide domain listing (SRS §29 domains + §40 API): a flat read-model over
// the existing per-project domain rows. No new owner of state — the domains
// table stays the durable truth (§61); this is only a join for the org-level
// view.
package store

import (
	"context"
	"log"
	"time"
)

// OrgDomainRow is one domain across all of an org's projects.
type OrgDomainRow struct {
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Domain      string    `json:"domain"`
	Verified    bool      `json:"verified"`
	CreatedAt   time.Time `json:"created_at"`
}

// domainStatusVerified maps the domains.status column to the boolean the
// API reports: the DomainManager writes "verified"/"unverified"/"pending",
// and "active" is the legacy v1 status for a working domain.
func domainStatusVerified(status string) bool {
	return status == "verified" || status == "active"
}

// ListOrgDomains returns every domain attached to the org's projects,
// oldest first. Unknown or empty org ids yield an empty list (never an
// error log — the API falls back to the single-org case via header or
// first-org, but a fresh install has neither).
func (s *Store) ListOrgDomains(orgID string) []OrgDomainRow {
	if orgID == "" || s.pool == nil {
		return []OrgDomainRow{}
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT d.project_id::text, p.name, d.domain, d.status, d.created_at
		FROM domains d
		JOIN projects p ON p.id = d.project_id
		WHERE p.org_id = $1
		ORDER BY d.created_at, d.domain`, orgID)
	if err != nil {
		log.Printf("store: list org domains for %s: %v", orgID, err)
		return nil
	}
	defer rows.Close()
	out := make([]OrgDomainRow, 0)
	for rows.Next() {
		var r OrgDomainRow
		var status string
		if err := rows.Scan(&r.ProjectID, &r.ProjectName, &r.Domain, &status, &r.CreatedAt); err != nil {
			continue
		}
		r.Verified = domainStatusVerified(status)
		out = append(out, r)
	}
	return out
}

// CountOrgDomains returns how many domains the org's projects carry.
func (s *Store) CountOrgDomains(orgID string) int {
	if orgID == "" || s.pool == nil {
		return 0
	}
	var n int
	if err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM domains d
		JOIN projects p ON p.id = d.project_id
		WHERE p.org_id = $1`, orgID).Scan(&n); err != nil {
		log.Printf("store: count org domains for %s: %v", orgID, err)
		return 0
	}
	return n
}
