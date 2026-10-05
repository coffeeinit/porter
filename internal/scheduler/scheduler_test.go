package scheduler

import "testing"

func TestPickNodeFiltersAndScores(t *testing.T) {
	nodes := []Node{
		{ID: "a", Ready: true, Arch: "amd64", VCPUFree: 4, MemFreeMiB: 4096},
		{ID: "b", Ready: false, Arch: "amd64", VCPUFree: 8, MemFreeMiB: 8192},
		{ID: "c", Ready: true, Arch: "arm64", VCPUFree: 8, MemFreeMiB: 8192},
	}
	if got := PickNode(nodes, Request{Arch: "amd64", VCPUs: 2, MemMiB: 512}); got != "a" {
		t.Fatalf("want a (only fit), got %q", got)
	}
	if got := PickNode(nodes, Request{VCPUs: 64, MemMiB: 64000}); got != "" {
		t.Fatalf("no fit must return empty, got %q", got)
	}
}
