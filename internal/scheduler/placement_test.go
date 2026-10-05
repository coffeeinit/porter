package scheduler

import "testing"

func TestPlacementTransitions(t *testing.T) {
	for _, e := range [][2]string{
		{PlacementNone, PlacementReserved},
		{PlacementReserved, PlacementActive},
		{PlacementActive, PlacementReleasing},
		{PlacementReleasing, PlacementReleased},
		{PlacementReleased, PlacementReserved},
	} {
		if err := Transition(e[0], e[1]); err != nil {
			t.Fatalf("%s->%s: %v", e[0], e[1], err)
		}
	}
	if err := Transition(PlacementNone, PlacementActive); err == nil {
		t.Fatal("none->active must fail")
	}
	if err := Transition(PlacementActive, PlacementReleased); err == nil {
		t.Fatal("must pass through releasing")
	}
}

func TestPreferHome(t *testing.T) {
	nodes := []Node{
		{ID: "n1", Ready: true, VCPUFree: 8, MemFreeMiB: 8000},
		{ID: "n2", Ready: true, VCPUFree: 8, MemFreeMiB: 8000},
	}
	req := Request{VCPUs: 2, MemMiB: 1024, PreferBinpack: true}
	if got := PreferHome(nodes, "n2", req); got != "n2" {
		t.Fatalf("fit home must win, got %q", got)
	}
	if got := PreferHome(nodes, "gone", req); got == "" {
		t.Fatal("missing home must fall back to scored pick")
	}
	small := []Node{{ID: "n1", Ready: true, VCPUFree: 1, MemFreeMiB: 100}}
	if got := PreferHome(small, "n1", req); got != "" {
		t.Fatalf("unfit home must not force, got %q", got)
	}
}

func TestEffective(t *testing.T) {
	if got := Effective(8, 1, 2.0); got != 14 {
		t.Fatalf("got %d", got)
	}
	if got := Effective(1, 4, 1.0); got != 0 {
		t.Fatalf("negative floors at 0, got %d", got)
	}
	if !Overcommitted(15, 14) || Overcommitted(14, 14) {
		t.Fatal("overcommit boundary broken")
	}
}
