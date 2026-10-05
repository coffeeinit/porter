package gateway

import "testing"

func TestPlanProjection(t *testing.T) {
	desired := []ProjectedRoute{{"a.example.com", "h1:8080"}, {"b.example.com", "h2:8080"}}
	projected := []ProjectedRoute{{"a.example.com", "h1:8080"}, {"b.example.com", "h9:8080"}, {"z.example.com", "h0:8080"}}
	p, err := PlanProjection(desired, projected)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Add) != 0 || len(p.Update) != 1 || len(p.Remove) != 1 {
		t.Fatalf("bad plan %+v", p)
	}
	if p.Update[0].Backend != "h2:8080" || p.Remove[0] != "z.example.com" {
		t.Fatalf("bad plan %+v", p)
	}
	if (Projection{}).Converged() != true {
		t.Fatal("empty plan converges")
	}
	if _, err := PlanProjection([]ProjectedRoute{{"", ""}}, nil); err == nil {
		t.Fatal("empty desired route must fail")
	}
	stable, err := PlanProjection(desired, desired)
	if err != nil || !stable.Converged() {
		t.Fatalf("identical must converge: %v", err)
	}
}

func TestTunnelReap(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "porter-vm-aaa", Hostname: "a.example.com", Provider: TunnelCloudflare},
		{ID: "porter-vm-bbb", Hostname: "gone.example.com", Provider: TunnelCloudflare},
		{ID: "other-xyz", Hostname: "gone.example.com", Provider: TunnelCloudflare},
	}
	live := map[string]bool{"a.example.com": true}
	got := Reap("porter-vm-", tunnels, live)
	if len(got) != 1 || got[0].ID != "porter-vm-bbb" {
		t.Fatalf("bad orphans %v", got)
	}
	if err := (Tunnel{ID: "x", Hostname: "h", Provider: "bogus"}).Validate(); err == nil {
		t.Fatal("unknown provider must fail")
	}
}
