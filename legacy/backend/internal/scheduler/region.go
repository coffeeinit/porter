// Region/zone topology for placement (SRS 17 scheduling inputs: region,
// zone, failure domain; strategies: topology spreading, failure-domain
// distribution). Pure policy over the existing filter inputs: nodes carry
// Region/Zone only when the caller actually knows them, so with everything
// empty the pre-topology behavior is reproduced exactly (single-node default
// path unchanged).
package scheduler

// regionAllowed applies the org's AllowedRegions policy as a hard filter:
// an empty policy admits everything; a set policy admits only nodes whose
// region is listed. A blank node region is NOT admitted under a set policy —
// an unverified region must not silently bypass org policy — so callers
// should only pass AllowedRegions when node regions are populated.
func regionAllowed(n Node, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, r := range allowed {
		if n.Region == r {
			return true
		}
	}
	return false
}

// zoneLoad counts the already-placed replicas of the same service in one
// zone (req.ExistingZones carries one entry per placed replica).
func zoneLoad(zones []string, zone string) int {
	n := 0
	for _, z := range zones {
		if z == zone {
			n++
		}
	}
	return n
}

// better ranks candidate a above candidate b; the filter has already run.
// Decisions must be reproducible from persisted state (SRS 17), so the terms
// are strictly ordered: the hard region filter dominates everything (it runs
// first, in PickNode/PreferHome), then zone spread, then the capacity/load
// score, then the node ID tie-break. With no ExistingZones the spread term
// is neutral and this reduces to the historical score-then-ID order.
func better(a, b Node, req Request) bool {
	if len(req.ExistingZones) > 0 {
		la, lb := zoneLoad(req.ExistingZones, a.Zone), zoneLoad(req.ExistingZones, b.Zone)
		if la != lb {
			return la < lb
		}
	}
	sa, sb := score(a, req), score(b, req)
	if sa != sb {
		return sa > sb
	}
	return a.ID < b.ID
}
