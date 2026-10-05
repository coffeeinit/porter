// Placement lifecycle: reserved → active → released (OCM-03). The
// scheduler decides; the controller persists. Soft release keeps
// home-node affinity + IP for fast restart; hard release frees everything.
// Cross-host restart without migration is an error the caller surfaces.
package scheduler

import "fmt"

// Placement states.
const (
	PlacementNone      = "none"
	PlacementReserved  = "reserved"
	PlacementActive    = "active"
	PlacementReleasing = "releasing"
	PlacementReleased  = "released"
)

// Transition validates placement edges. ReleaseSoft keeps affinity (maps to
// released-with-affinity); ReleaseHard clears it. Both pass through
// releasing so the controller can clean routes/volumes exactly once.
func Transition(cur, next string) error {
	switch cur {
	case PlacementNone:
		if next != PlacementReserved {
			return fmt.Errorf("scheduler: none -> %s illegal", next)
		}
	case PlacementReserved:
		if next != PlacementActive && next != PlacementReleasing {
			return fmt.Errorf("scheduler: reserved -> %s illegal", next)
		}
	case PlacementActive:
		if next != PlacementReleasing {
			return fmt.Errorf("scheduler: active -> %s illegal", next)
		}
	case PlacementReleasing:
		if next != PlacementReleased {
			return fmt.Errorf("scheduler: releasing -> %s illegal", next)
		}
	case PlacementReleased:
		if next != PlacementReserved {
			return fmt.Errorf("scheduler: released -> %s illegal (re-reserve only)", next)
		}
	default:
		return fmt.Errorf("scheduler: unknown placement %q", cur)
	}
	return nil
}

// PreferHome implements affinity-first selection (OCM-03): when the workload
// ran before, try the home node when it still fits; otherwise fall back to
// PickNode. Affinity never overrides a failed filter — an unfit home node
// yields a normal scored pick, and the caller records the move.
func PreferHome(nodes []Node, homeID string, req Request) string {
	if homeID == "" {
		return PickNode(nodes, req)
	}
	for _, n := range nodes {
		if n.ID != homeID {
			continue
		}
		if !n.Ready {
			break
		}
		if !regionAllowed(n, req.AllowedRegions) {
			break
		}
		if req.Arch != "" && n.Arch != "" && n.Arch != req.Arch {
			break
		}
		if n.VCPUFree < req.VCPUs || n.MemFreeMiB < req.MemMiB {
			break
		}
		if !tolerates(n.Taints, req.Tolerations) || !matches(n.Labels, req.Labels) {
			break
		}
		return n.ID
	}
	return PickNode(nodes, req)
}
