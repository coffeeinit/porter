// Label-group affinity (FCM-14 remainder): two-level grouping for
// placement — workloads carrying the same group label prefer the group's
// home node when it fits, spreading otherwise. Pure policy; the scheduler
// and placement controller share it.
package scheduler

// GroupAffinity pins one label group to a home node.
type GroupAffinity struct {
	Group  string // label value, e.g. "team:payments"
	HomeID string // preferred node id
}

// PreferGroup returns the home node when the workload carries the group
// label and the home node is in the candidate set; otherwise "". Callers
// fall back to PreferHome/PickNode, so affinity never forces an unfit node.
func PreferGroup(nodes []Node, workloadLabels map[string]string, groups []GroupAffinity) string {
	if len(workloadLabels) == 0 || len(groups) == 0 {
		return ""
	}
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	for _, g := range groups {
		if g.Group == "" || !ids[g.HomeID] {
			continue
		}
		for k, v := range workloadLabels {
			if k == "group" && v == g.Group {
				return g.HomeID
			}
		}
	}
	return ""
}
