// Route projection plan (OCM-08 remainder): the pure diff between desired
// routes (Postgres truth) and projected routes (cache/gateway). The gateway
// applies the plan; the reconciler repairs drift on its tick. Cache stays a
// write-through detail, never the truth.
package gateway

import (
	"fmt"
	"sort"
)

// ProjectedRoute is one desired hostname → backend mapping.
type ProjectedRoute struct {
	Hostname string // e.g. m-web-prod.example.com
	Backend  string // host:port or tunnel id
}

// Projection is the deterministic add/update/remove plan.
type Projection struct {
	Add    []ProjectedRoute
	Update []ProjectedRoute
	Remove []string // hostnames to withdraw
}

// PlanProjection diffs desired against projected. Same hostname + same
// backend = converged (no-op); same hostname + different backend = update;
// missing = add; extra = remove. Output is sorted for stable apply.
func PlanProjection(desired, projected []ProjectedRoute) (Projection, error) {
	want := map[string]string{}
	for _, r := range desired {
		if r.Hostname == "" || r.Backend == "" {
			return Projection{}, fmt.Errorf("gateway: desired route needs hostname and backend")
		}
		want[r.Hostname] = r.Backend
	}
	have := map[string]string{}
	for _, r := range projected {
		if r.Hostname == "" {
			continue
		}
		have[r.Hostname] = r.Backend
	}
	var p Projection
	for h, b := range want {
		if hb, ok := have[h]; !ok {
			p.Add = append(p.Add, ProjectedRoute{h, b})
		} else if hb != b {
			p.Update = append(p.Update, ProjectedRoute{h, b})
		}
	}
	for h := range have {
		if _, ok := want[h]; !ok {
			p.Remove = append(p.Remove, h)
		}
	}
	byHost := func(a, b ProjectedRoute) bool { return a.Hostname < b.Hostname }
	sort.Slice(p.Add, func(i, j int) bool { return byHost(p.Add[i], p.Add[j]) })
	sort.Slice(p.Update, func(i, j int) bool { return byHost(p.Update[i], p.Update[j]) })
	sort.Strings(p.Remove)
	return p, nil
}

// Converged reports an empty plan.
func (p Projection) Converged() bool {
	return len(p.Add) == 0 && len(p.Update) == 0 && len(p.Remove) == 0
}
