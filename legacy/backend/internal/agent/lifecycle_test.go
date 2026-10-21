package agent

import "testing"

func TestUpgradeFlow(t *testing.T) {
	u := UpgradeIntent{NodeID: "n1", FromVersion: "v1", ToVersion: "v2"}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{UpgradeWanted, UpgradeStaged, UpgradeActive, UpgradeRolledBk} {
		if err := u.Advance(); err != nil {
			t.Fatal(err)
		}
		if u.State != want {
			t.Fatalf("got %q want %q", u.State, want)
		}
	}
	if err := u.Advance(); err == nil {
		t.Fatal("terminal state must stick")
	}
	if err := (UpgradeIntent{NodeID: "n", FromVersion: "v", ToVersion: "v"}).Validate(); err == nil {
		t.Fatal("same-version upgrade must fail")
	}
}

func TestBootstrapIdempotent(t *testing.T) {
	b := Bootstrap{NodeID: "n1", Version: "v3", Stamp: true}
	if got := b.Apply(); got != BootstrapAlready {
		t.Fatalf("stamp must short-circuit, got %q", got)
	}
	c := Bootstrap{NodeID: "n1", Version: "v3"}
	if got := c.Apply(); got != BootstrapPending {
		t.Fatalf("fresh must pend, got %q", got)
	}
	if got := c.Report(nil); got != BootstrapApplied || !c.Stamp {
		t.Fatal("success must stamp")
	}
}
