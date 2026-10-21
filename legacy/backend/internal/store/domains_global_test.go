package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"porter/internal/types"
)

// TestListOrgDomainsPG covers the org-wide read-model over the per-project
// domain rows: join for project names, verified mapping from the status
// column, org isolation, and the count. Requires Postgres (skips without
// PORTER_TEST_DATABASE_URL, same convention as the billing tests).
func TestListOrgDomainsPG(t *testing.T) {
	s := pgStore(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	orgID := uuid.NewString()
	if err := s.PutOrg(&types.Org{ID: orgID, Name: "dom-org-" + suffix}); err != nil {
		t.Fatalf("put org: %v", err)
	}
	projID := uuid.NewString()
	projName := "dom-proj-" + suffix
	s.PutProject(&types.Project{ID: projID, Name: projName, OrgID: orgID})
	otherOrgID := uuid.NewString()
	if err := s.PutOrg(&types.Org{ID: otherOrgID, Name: "dom-org-other-" + suffix}); err != nil {
		t.Fatalf("put other org: %v", err)
	}
	otherProjID := uuid.NewString()
	s.PutProject(&types.Project{ID: otherProjID, Name: "dom-proj-other-" + suffix, OrgID: otherOrgID})
	// Shared database: drop the fixtures even on failure. The org delete
	// cascades to projects and domains, but the explicit domain/project
	// deletes keep the cleanup honest if those FK cascades ever change.
	t.Cleanup(func() {
		p := s.pool
		_, _ = p.Exec(context.Background(),
			`DELETE FROM domains WHERE project_id::text = ANY($1)`, []string{projID, otherProjID})
		_, _ = p.Exec(context.Background(),
			`DELETE FROM projects WHERE id::text = ANY($1)`, []string{projID, otherProjID})
		_, _ = p.Exec(context.Background(),
			`DELETE FROM orgs WHERE id::text = ANY($1)`, []string{orgID, otherOrgID})
	})

	verified := &types.Domain{ProjectID: projID, Domain: "ok-" + suffix + ".example.com", Type: "custom", Status: "verified"}
	s.AddDomain(projID, verified)
	pending := &types.Domain{ProjectID: projID, Domain: "pending-" + suffix + ".example.com", Type: "custom", Status: "pending"}
	s.AddDomain(projID, pending)
	s.AddDomain(otherProjID, &types.Domain{ProjectID: otherProjID, Domain: "other-" + suffix + ".example.com", Type: "custom", Status: "verified"})

	rows := s.ListOrgDomains(orgID)
	if len(rows) != 2 {
		t.Fatalf("ListOrgDomains returned %d rows, want 2: %+v", len(rows), rows)
	}
	byDomain := map[string]OrgDomainRow{}
	for _, r := range rows {
		byDomain[r.Domain] = r
	}
	got, ok := byDomain[verified.Domain]
	if !ok {
		t.Fatalf("verified domain %q missing from org list: %+v", verified.Domain, rows)
	}
	if got.ProjectID != projID || got.ProjectName != projName {
		t.Fatalf("project join wrong: got project %q (%q), want %q (%q)", got.ProjectID, got.ProjectName, projID, projName)
	}
	if !got.Verified {
		t.Fatalf("status %q must map to verified=true: %+v", "verified", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("created_at not populated: %+v", got)
	}
	if p, ok := byDomain[pending.Domain]; !ok {
		t.Fatalf("pending domain %q missing: %+v", pending.Domain, rows)
	} else if p.Verified {
		t.Fatalf("status %q must map to verified=false: %+v", "pending", p)
	}

	// Org isolation: the other org sees only its own row.
	other := s.ListOrgDomains(otherOrgID)
	if len(other) != 1 || other[0].Domain != "other-"+suffix+".example.com" {
		t.Fatalf("other org list wrong: %+v", other)
	}
	if n := len(s.ListOrgDomains("")); n != 0 {
		t.Fatalf("empty org id must yield no rows, got %d (org fallback returns first org, but empty stays empty)", n)
	}
	if n := len(s.ListOrgDomains(uuid.NewString())); n != 0 {
		t.Fatalf("unknown org id must yield no rows, got %d", n)
	}

	// Count tracks the same set the listing returns.
	if got, want := s.CountOrgDomains(orgID), 2; got != want {
		t.Fatalf("CountOrgDomains = %d, want %d", got, want)
	}
	if got := s.CountOrgDomains(otherOrgID); got != 1 {
		t.Fatalf("other org count = %d, want 1", got)
	}
	if got := s.CountOrgDomains(uuid.NewString()); got != 0 {
		t.Fatalf("unknown org count = %d, want 0", got)
	}
}
