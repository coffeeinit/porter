// Preview sweep: reaps stale preview deployments that no longer serve
// traffic. A preview deployment older than the TTL with zero healthy VMs
// has its VM rows, preview domain, and matching preview environments
// removed. No runtime access: rows only, never stop/boot. Production
// deployments are never touched.
package controller

import (
	"strings"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// PreviewStore is the minimal surface the preview sweep needs. The concrete
// *store.Store satisfies it (see assertion below); signatures mirror the
// real store methods exactly.
type PreviewStore interface {
	ListProjects() []*types.Project
	ListDeployments(projectID string) []*types.Deployment
	GetVM(id string) (*types.VM, bool)
	DeleteVM(id string)
	DeleteDomain(projectID, domain string)
	ListEnvironments(projectID string) []*types.Environment
	DeleteEnvironment(id string) bool
}

var _ PreviewStore = (*store.Store)(nil)

// defaultPreviewTTL is the staleness horizon for the tick-driven sweep.
const defaultPreviewTTL = 7 * 24 * time.Hour

// lastPreviewSweep throttles the tick-driven sweep (same pattern as
// maybeBackupScan/maybeRollup: at most one sweep per interval).
var lastPreviewSweep time.Time

// maybePreviewSweep runs SweepPreviews off r.tasks at most once per hour.
// Nil-safe: a nil runner, nil tasks, or a tasks ledger that does not expose
// the preview surface skips honestly.
func (r *TaskRunner) maybePreviewSweep(now time.Time) {
	if r == nil || r.tasks == nil {
		return
	}
	if !lastPreviewSweep.IsZero() && now.Sub(lastPreviewSweep) < time.Hour {
		return
	}
	ps, ok := r.tasks.(PreviewStore)
	if !ok || ps == nil {
		return
	}
	lastPreviewSweep = now
	_, _ = SweepPreviews(ps, defaultPreviewTTL, now)
}

// SkipPreviewBuild reports whether a head-commit message asks CI/CD to stand
// down. Matches [skip ci] / [skip cd] case-insensitively. Single source of
// truth shared with the GitHub webhook (api/feature_gitops.go delegates here)
// so skip semantics stay identical for builds and preview envs.
func SkipPreviewBuild(msg string) (bool, string) {
	lower := strings.ToLower(msg)
	for _, tag := range []string{"[skip ci]", "[skip cd]"} {
		if strings.Contains(lower, tag) {
			return true, "head commit message contains " + tag
		}
	}
	return false, ""
}

// SweepPreviews deletes VM rows, the preview domain, and matching preview
// environments for preview (never production) deployments older than ttl
// with zero healthy VMs. It returns the swept deployment IDs.
func SweepPreviews(st PreviewStore, ttl time.Duration, now time.Time) (swept []string, err error) {
	if st == nil {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if ttl < 0 {
		ttl = 0
	}
	for _, p := range st.ListProjects() {
		if p == nil {
			continue
		}
		for _, d := range st.ListDeployments(p.ID) {
			if d == nil {
				continue
			}
			if d.IsProduction {
				continue
			}
			env := strings.ToLower(strings.TrimSpace(d.Environment))
			if env != "preview" {
				continue
			}
			if now.Sub(d.CreatedAt) <= ttl {
				continue
			}
			healthy := 0
			for _, vmID := range d.VMIDs {
				if vm, ok := st.GetVM(vmID); ok && vm != nil && vm.HealthStatus == types.HealthHealthy {
					healthy++
				}
			}
			if healthy > 0 {
				continue
			}
			for _, vmID := range d.VMIDs {
				if _, ok := st.GetVM(vmID); ok {
					st.DeleteVM(vmID)
				}
			}
			bare := previewBare(d.PreviewURL)
			if bare != "" {
				st.DeleteDomain(d.ProjectID, bare)
				for _, e := range st.ListEnvironments(d.ProjectID) {
					if e == nil {
						continue
					}
					if e.EnvDomain == bare || previewBare(e.URL) == bare || e.URL == d.PreviewURL {
						st.DeleteEnvironment(e.ID)
					}
				}
			}
			swept = append(swept, d.ID)
		}
	}
	return swept, nil
}

// previewBare strips any URL scheme, returning the bare routable host.
func previewBare(raw string) string {
	s := strings.TrimSpace(raw)
	if u := strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://"); u != s {
		return strings.Split(u, "/")[0]
	}
	return strings.Split(s, "/")[0]
}
