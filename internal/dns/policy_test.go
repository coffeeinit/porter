package dns

import "testing"

func TestVisible(t *testing.T) {
	pols := []Policy{{Zone: "svc.cluster.local", Internal: true}, {Zone: "example.com"}}
	if !Visible(pols, "svc.cluster.local", ViewInternal) {
		t.Fatal("internal zone must answer internally")
	}
	if Visible(pols, "svc.cluster.local", ViewExternal) {
		t.Fatal("internal zone must not leak externally")
	}
	if !Visible(pols, "example.com", ViewExternal) {
		t.Fatal("public zone must answer everywhere")
	}
	if Visible(pols, "unknown.test", ViewInternal) {
		t.Fatal("unknown zones fail closed")
	}
}

func TestSearchDomains(t *testing.T) {
	got := SearchDomains("shop")
	if len(got) != 2 || got[0] != "shop.svc.cluster.local." {
		t.Fatalf("bad search path: %v", got)
	}
}
