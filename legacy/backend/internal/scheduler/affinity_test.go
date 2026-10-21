package scheduler

import "testing"

func TestPreferGroup(t *testing.T) {
	nodes := []Node{
		{ID: "n1", Ready: true, VCPUFree: 8, MemFreeMiB: 8000},
		{ID: "n2", Ready: true, VCPUFree: 8, MemFreeMiB: 8000},
	}
	groups := []GroupAffinity{{Group: "payments", HomeID: "n2"}}
	if got := PreferGroup(nodes, map[string]string{"group": "payments"}, groups); got != "n2" {
		t.Fatalf("group home must win, got %q", got)
	}
	if got := PreferGroup(nodes, map[string]string{"group": "other"}, groups); got != "" {
		t.Fatalf("unknown group must yield, got %q", got)
	}
	if got := PreferGroup(nodes, map[string]string{"group": "payments"},
		[]GroupAffinity{{Group: "payments", HomeID: "gone"}}); got != "" {
		t.Fatalf("absent home must yield, got %q", got)
	}
	if got := PreferGroup(nodes, nil, groups); got != "" {
		t.Fatal("unlabeled workload must yield")
	}
}
