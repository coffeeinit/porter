// Package scheduler owns placement decisions (filter → score → persist).
// Execution stays in runtime/agent; this package never boots anything. It is
// deliberately pure (no store I/O) so controllers + admission share one policy.
package scheduler

import "sort"

// Node is the schedulable capacity unit (mirrors types.Server fields needed).
type Node struct {
	ID          string
	Ready       bool
	Arch        string
	VCPUFree    int
	MemFreeMiB  int
	Labels      map[string]string
	Taints      []string
	CurrentLoad float64
	// Failure-domain topology (SRS 17). Both optional: callers that don't
	// know a node's placement leave them blank, which disables the region
	// filter and zone spread for that node.
	Region string
	Zone   string
}

// Request is one placement ask.
type Request struct {
	Arch          string
	VCPUs         int
	MemMiB        int
	Labels        map[string]string
	Tolerations   []string
	PreferBinpack bool
	// AllowedRegions is org region policy (resource OrgSettings): when
	// non-empty, only nodes whose Region is listed pass the filter. Pass it
	// only when node regions are populated — blank regions never match.
	AllowedRegions []string
	// ExistingZones carries one zone entry per already-placed replica of the
	// same service; scoring prefers the zone with the fewest (topology
	// spreading). Empty disables spreading.
	ExistingZones []string
}

// PickNode filters (region policy, ready, arch, capacity, taints) then
// scores (topology spread + binpack/spread + load) and returns the winner.
// Empty string = no fit (caller must surface an explicit condition, never
// silently queue).
func PickNode(nodes []Node, req Request) string {
	cands := []Node{}
	for _, n := range nodes {
		if !n.Ready {
			continue
		}
		if !regionAllowed(n, req.AllowedRegions) {
			continue
		}
		if req.Arch != "" && n.Arch != "" && n.Arch != req.Arch {
			continue
		}
		if n.VCPUFree < req.VCPUs || n.MemFreeMiB < req.MemMiB {
			continue
		}
		if !tolerates(n.Taints, req.Tolerations) {
			continue
		}
		if !matches(n.Labels, req.Labels) {
			continue
		}
		cands = append(cands, n)
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		return better(cands[i], cands[j], req)
	})
	return cands[0].ID
}

func tolerates(taints, tol []string) bool {
	have := map[string]bool{}
	for _, t := range tol {
		have[t] = true
	}
	for _, t := range taints {
		if !have[t] {
			return false
		}
	}
	return true
}

func matches(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// score: binpack prefers fullest fitting node; spread prefers emptiest.
// Load always penalizes so hot nodes drain first.
func score(n Node, req Request) float64 {
	used := 1.0 - float64(n.VCPUFree)/float64(max(n.VCPUFree+req.VCPUs, 1))
	s := used - n.CurrentLoad
	if !req.PreferBinpack {
		s = -s
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
