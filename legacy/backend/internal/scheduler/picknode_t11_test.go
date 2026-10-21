package scheduler

import (
	"testing"
)

// T11 hermetic PickNode filter + determinism tests. Pure policy, no I/O.

func TestT11PickNodeReadyArchCapacity(t *testing.T) {
	nodes := []Node{
		{ID: "down", Ready: false, Arch: "amd64", VCPUFree: 64, MemFreeMiB: 262144},
		{ID: "wrong-arch", Ready: true, Arch: "arm64", VCPUFree: 64, MemFreeMiB: 262144},
		{ID: "thin-cpu", Ready: true, Arch: "amd64", VCPUFree: 1, MemFreeMiB: 262144},
		{ID: "thin-mem", Ready: true, Arch: "amd64", VCPUFree: 64, MemFreeMiB: 128},
		{ID: "fit", Ready: true, Arch: "amd64", VCPUFree: 4, MemFreeMiB: 4096},
	}
	req := Request{Arch: "amd64", VCPUs: 2, MemMiB: 1024}
	if got := PickNode(nodes, req); got != "fit" {
		t.Fatalf("only fit node must win, got %q", got)
	}
	// Exact fit is a fit (strict < comparison in the filter).
	exact := []Node{{ID: "exact", Ready: true, Arch: "amd64", VCPUFree: 2, MemFreeMiB: 1024}}
	if got := PickNode(exact, req); got != "exact" {
		t.Fatalf("exact-capacity node must fit, got %q", got)
	}
	// One vCPU short is no fit.
	short := []Node{{ID: "short", Ready: true, Arch: "amd64", VCPUFree: 1, MemFreeMiB: 1024}}
	if got := PickNode(short, req); got != "" {
		t.Fatalf("under-capacity node must yield empty, got %q", got)
	}
	// Blank node arch never blocks an arch ask.
	blankArch := []Node{{ID: "blank", Ready: true, VCPUFree: 4, MemFreeMiB: 4096}}
	if got := PickNode(blankArch, req); got != "blank" {
		t.Fatalf("blank node arch must not filter, got %q", got)
	}
	// Blank request arch accepts any node arch.
	mixed := []Node{
		{ID: "x86", Ready: true, Arch: "amd64", VCPUFree: 4, MemFreeMiB: 4096},
		{ID: "arm", Ready: true, Arch: "arm64", VCPUFree: 4, MemFreeMiB: 4096},
	}
	if got := PickNode(mixed, Request{VCPUs: 1, MemMiB: 512}); got == "" {
		t.Fatal("arch-agnostic request must match")
	}
}

func TestT11PickNodeTaints(t *testing.T) {
	nodes := []Node{
		{ID: "tainted", Ready: true, VCPUFree: 8, MemFreeMiB: 8192, Taints: []string{"gpu"}},
		{ID: "plain", Ready: true, VCPUFree: 8, MemFreeMiB: 8192},
	}
	base := Request{VCPUs: 1, MemMiB: 512}
	if got := PickNode(nodes, base); got != "plain" {
		t.Fatalf("untolerated taint must filter, got %q", got)
	}
	tolerating := base
	tolerating.Tolerations = []string{"gpu"}
	if got := PickNode(nodes, tolerating); got == "" {
		t.Fatal("tolerated taint must stay eligible")
	}
	// Partial toleration still filters the tainted node.
	multi := []Node{
		{ID: "multi", Ready: true, VCPUFree: 8, MemFreeMiB: 8192, Taints: []string{"gpu", "spot"}},
		{ID: "plain", Ready: true, VCPUFree: 8, MemFreeMiB: 8192},
	}
	partial := base
	partial.Tolerations = []string{"gpu"}
	if got := PickNode(multi, partial); got != "plain" {
		t.Fatalf("partial toleration must still filter, got %q", got)
	}
	full := base
	full.Tolerations = []string{"gpu", "spot"}
	if got := PickNode(multi, full); got == "" {
		t.Fatal("full toleration must keep both candidates")
	}
}

func TestT11PickNodeLabels(t *testing.T) {
	nodes := []Node{
		{ID: "zone-a", Ready: true, VCPUFree: 8, MemFreeMiB: 8192, Labels: map[string]string{"zone": "a"}},
		{ID: "zone-b", Ready: true, VCPUFree: 8, MemFreeMiB: 8192, Labels: map[string]string{"zone": "b"}},
		{ID: "unlabeled", Ready: true, VCPUFree: 8, MemFreeMiB: 8192},
	}
	req := Request{VCPUs: 1, MemMiB: 512, Labels: map[string]string{"zone": "b"}}
	if got := PickNode(nodes, req); got != "zone-b" {
		t.Fatalf("label selector must pick zone-b, got %q", got)
	}
	missing := Request{VCPUs: 1, MemMiB: 512, Labels: map[string]string{"zone": "c"}}
	if got := PickNode(nodes, missing); got != "" {
		t.Fatalf("unsatisfiable selector must yield empty, got %q", got)
	}
	// Empty selector matches everything (nil and populated labels alike).
	if got := PickNode(nodes, Request{VCPUs: 1, MemMiB: 512}); got == "" {
		t.Fatal("empty selector must match")
	}
	// A node with nil labels never satisfies a non-empty selector.
	nils := []Node{{ID: "nil-labels", Ready: true, VCPUFree: 8, MemFreeMiB: 8192}}
	if got := PickNode(nils, req); got != "" {
		t.Fatalf("nil labels must not match selector, got %q", got)
	}
}

func TestT11PickNodeDeterministicTieBreak(t *testing.T) {
	mk := func(order ...string) []Node {
		out := make([]Node, 0, len(order))
		for _, id := range order {
			out = append(out, Node{ID: id, Ready: true, Arch: "amd64", VCPUFree: 8, MemFreeMiB: 8192})
		}
		return out
	}
	// Identical capacity: lowest ID wins regardless of input order.
	req := Request{Arch: "amd64", VCPUs: 1, MemMiB: 512}
	if got := PickNode(mk("node-b", "node-a"), req); got != "node-a" {
		t.Fatalf("tie must break on lowest ID, got %q", got)
	}
	if got := PickNode(mk("node-a", "node-b"), req); got != "node-a" {
		t.Fatalf("tie-break must be input-order independent, got %q", got)
	}
	// Repeated calls are stable for both scoring modes.
	spread := mk("n2", "n1", "n3")
	first := PickNode(spread, req)
	for i := 0; i < 10; i++ {
		if got := PickNode(spread, req); got != first {
			t.Fatalf("spread picks must be stable: first %q, now %q", first, got)
		}
	}
	binpack := req
	binpack.PreferBinpack = true
	firstBin := PickNode(spread, binpack)
	for i := 0; i < 10; i++ {
		if got := PickNode(spread, binpack); got != firstBin {
			t.Fatalf("binpack picks must be stable: first %q, now %q", firstBin, got)
		}
	}
}
