package scheduler

import "testing"

// Region/zone topology tests (SRS 17): region policy is a hard filter, zone
// spreading is a score that it outranks, and all decisions stay
// reproducible. Pure policy, no I/O.

func TestPickNodeRegionFilter(t *testing.T) {
	nodes := []Node{
		{ID: "r1-small", Ready: true, Region: "r1", VCPUFree: 2, MemFreeMiB: 2048},
		{ID: "r2-big", Ready: true, Region: "r2", VCPUFree: 64, MemFreeMiB: 65536},
		{ID: "blank", Ready: true, VCPUFree: 64, MemFreeMiB: 65536},
	}
	req := Request{VCPUs: 1, MemMiB: 512, AllowedRegions: []string{"r1"}}
	// r2-big and blank would win on capacity/score, but org policy admits
	// only r1 — including against blank-region nodes.
	if got := PickNode(nodes, req); got != "r1-small" {
		t.Fatalf("org region policy must filter to r1, got %q", got)
	}
	// Blank regions count as outside the policy.
	onlyBlank := []Node{{ID: "blank", Ready: true, VCPUFree: 8, MemFreeMiB: 8192}}
	if got := PickNode(onlyBlank, req); got != "" {
		t.Fatalf("blank node region must not bypass org policy, got %q", got)
	}
	// Empty policy admits everything (historical behavior).
	if got := PickNode(nodes, Request{VCPUs: 1, MemMiB: 512}); got == "" {
		t.Fatal("empty AllowedRegions must not filter")
	}
	// No admissible region at all → explicit empty result.
	if got := PickNode(nodes, Request{VCPUs: 1, MemMiB: 512, AllowedRegions: []string{"r9"}}); got != "" {
		t.Fatalf("unsatisfiable region policy must yield empty, got %q", got)
	}
}

func TestPickNodeZoneSpread(t *testing.T) {
	// Two replicas of the service already live in zone z1.
	nodes := []Node{
		{ID: "z1a", Ready: true, Zone: "z1", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "z1b", Ready: true, Zone: "z1", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "z2a", Ready: true, Zone: "z2", VCPUFree: 8, MemFreeMiB: 8192},
	}
	req := Request{VCPUs: 1, MemMiB: 512, ExistingZones: []string{"z1", "z1"}}
	if got := PickNode(nodes, req); got != "z2a" {
		t.Fatalf("placement must prefer the zone with fewest replicas, got %q", got)
	}
	// After z2 takes one replica it is still emptier than z1 (2 vs 1).
	req.ExistingZones = []string{"z1", "z1", "z2"}
	if got := PickNode(nodes, req); got != "z2a" {
		t.Fatalf("spreading must keep preferring the lighter zone, got %q", got)
	}
	// Balanced zones (2 vs 2): tie falls through to score then node ID.
	req.ExistingZones = []string{"z1", "z1", "z2", "z2"}
	if got := PickNode(nodes, req); got != "z1a" {
		t.Fatalf("balanced spread must fall back to the ID tie-break, got %q", got)
	}
	// Without ExistingZones the spread term is neutral: identical nodes
	// resolve to the lowest ID regardless of their zone.
	if got := PickNode(nodes, Request{VCPUs: 1, MemMiB: 512}); got != "z1a" {
		t.Fatalf("no spread input must reduce to the plain tie-break, got %q", got)
	}
	// Replicas placed on zone-less nodes count as their own domain: a
	// zone-less node already holding replicas loses against an empty zone.
	mixed := []Node{
		{ID: "nozone", Ready: true, VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "zoned", Ready: true, Zone: "z1", VCPUFree: 8, MemFreeMiB: 8192},
	}
	spread := Request{VCPUs: 1, MemMiB: 512, ExistingZones: []string{""}}
	if got := PickNode(mixed, spread); got != "zoned" {
		t.Fatalf("zone-less replicas must load their own domain, got %q", got)
	}
}

func TestPickNodeRegionOutranksZoneSpread(t *testing.T) {
	// The emptiest zone sits in a disallowed region; policy wins anyway.
	nodes := []Node{
		{ID: "r1-busy", Ready: true, Region: "r1", Zone: "z-loaded", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "r2-empty", Ready: true, Region: "r2", Zone: "z-empty", VCPUFree: 8, MemFreeMiB: 8192},
	}
	req := Request{
		VCPUs:          1,
		MemMiB:         512,
		AllowedRegions: []string{"r1"},
		ExistingZones:  []string{"z-loaded", "z-loaded", "z-loaded"},
	}
	if got := PickNode(nodes, req); got != "r1-busy" {
		t.Fatalf("region policy must outweigh zone spread, got %q", got)
	}
}

func TestPickNodeTopologyZeroChangeWhenEmpty(t *testing.T) {
	// Same fleet, one with topology fields populated, one stripped: with
	// empty Region/Zone/AllowedRegions/ExistingZones both must decide
	// identically, and repeatedly so.
	with := []Node{
		{ID: "n2", Ready: true, Region: "r", Zone: "z", VCPUFree: 4, MemFreeMiB: 4096},
		{ID: "n1", Ready: true, Region: "r", Zone: "z", VCPUFree: 8, MemFreeMiB: 8192},
	}
	without := make([]Node, len(with))
	for i, n := range with {
		n.Region, n.Zone = "", ""
		without[i] = n
	}
	req := Request{Arch: "amd64", VCPUs: 2, MemMiB: 1024}
	want := PickNode(without, req)
	for i := 0; i < 10; i++ {
		if got := PickNode(with, req); got != want {
			t.Fatalf("topology-empty input must not change the pick: want %q, got %q", want, got)
		}
	}
	// Determinism is input-order independent (SRS 17).
	reversed := []Node{with[1], with[0]}
	if got := PickNode(reversed, req); got != want {
		t.Fatalf("pick must be input-order independent: want %q, got %q", want, got)
	}
}

func TestPreferHomeRespectsRegionPolicy(t *testing.T) {
	// The home node now sits outside the org's regions: affinity must not
	// override the filter, and the fallback pick must honor it too.
	nodes := []Node{
		{ID: "home-r2", Ready: true, Region: "r2", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "other-r2", Ready: true, Region: "r2", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "fit-r1", Ready: true, Region: "r1", VCPUFree: 4, MemFreeMiB: 4096},
	}
	req := Request{Arch: "amd64", VCPUs: 2, MemMiB: 1024, AllowedRegions: []string{"r1"}}
	if got := PreferHome(nodes, "home-r2", req); got != "fit-r1" {
		t.Fatalf("affinity must yield to region policy, got %q", got)
	}
}
