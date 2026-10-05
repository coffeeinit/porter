package deployment

import "testing"

func TestTemplate(t *testing.T) {
	ok := Template{Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Source: "debian@trixie"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if !ok.Collectible() {
		t.Fatal("zero refs must collect")
	}
	pinned := ok
	pinned.RefCount = 3
	if pinned.Collectible() {
		t.Fatal("pinned base must not collect")
	}
	if got := CloneDecision(true, true, false); got != CloneLinked {
		t.Fatalf("same-node present must link, got %q", got)
	}
	for _, c := range []struct {
		same, base, mig bool
	}{
		{false, true, false}, {true, false, false}, {true, true, true},
	} {
		if got := CloneDecision(c.same, c.base, c.mig); got != CloneFull {
			t.Fatalf("must go full: %+v got %q", c, got)
		}
	}
}
