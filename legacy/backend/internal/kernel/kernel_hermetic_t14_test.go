package kernel

import (
	"strings"
	"testing"
)

func TestT14BuildLegalChainProgress(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.99")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != BuildPending || b.Progress != 0 {
		t.Fatalf("fresh build must be pending/0, got %s/%d", b.State, b.Progress)
	}
	steps := []struct {
		next     string
		progress int
	}{
		{BuildFetching, 10},
		{BuildConfiguring, 40},
		{BuildCompiling, 70},
		{BuildCompleted, 100},
	}
	for _, s := range steps {
		if err := b.Advance(s.next, s.progress); err != nil {
			t.Fatalf("advance to %s: %v", s.next, err)
		}
		if b.State != s.next || b.Progress != s.progress {
			t.Fatalf("want %s/%d got %s/%d", s.next, s.progress, b.State, b.Progress)
		}
	}
}

func TestT14BuildFailedEdgesTerminal(t *testing.T) {
	// Every non-terminal state may fail.
	for _, from := range []string{BuildPending, BuildFetching, BuildConfiguring, BuildCompiling} {
		b := &Build{Version: "6.18.99", State: from}
		if err := b.Advance(BuildFailed, 5); err != nil {
			t.Fatalf("%s -> failed must be legal: %v", from, err)
		}
		if err := b.Advance(BuildFetching, 0); err == nil {
			t.Fatalf("failed state must stick (from %s)", from)
		}
		if err := b.Advance(BuildCompleted, 100); err == nil {
			t.Fatalf("failed must never complete (from %s)", from)
		}
	}
	// Completed is sticky too.
	b := &Build{Version: "6.18.99", State: BuildCompleted, Progress: 100}
	if err := b.Advance(BuildFailed, 0); err == nil {
		t.Fatal("completed must not transition to failed")
	}
}

func TestT14BuildIllegalSkips(t *testing.T) {
	cases := []struct {
		from string
		next string
	}{
		{"", BuildFetching},            // must start at pending
		{BuildPending, BuildCompiling}, // skip-ahead
		{BuildPending, BuildCompleted},
		{BuildFetching, BuildCompleted},
		{BuildFetching, BuildCompiling},
		{BuildConfiguring, BuildCompleted},
		{BuildConfiguring, BuildFetching}, // backward
		{BuildCompiling, BuildFetching},   // backward
		{BuildPending, "bogus"},
		{BuildCompleted, BuildPending},
		{BuildFailed, BuildPending},
	}
	for _, c := range cases {
		b := &Build{Version: "6.18.99", State: c.from}
		if err := b.Advance(c.next, 0); err == nil {
			t.Fatalf("%q -> %q must fail", c.from, c.next)
		}
	}
	// Empty state only accepts pending.
	b := &Build{Version: "6.18.99"}
	if err := b.Advance(BuildPending, 0); err != nil {
		t.Fatalf("empty -> pending must be legal: %v", err)
	}
}

func TestT14ManagerPerVersionIsolation(t *testing.T) {
	m := NewManager()
	if _, err := m.Start("6.18.1"); err != nil {
		t.Fatal(err)
	}
	// Different version builds concurrently without interference.
	if _, err := m.Start("6.18.2"); err != nil {
		t.Fatalf("distinct versions must not conflict: %v", err)
	}
	// Same version still guarded.
	if _, err := m.Start("6.18.1"); err == nil {
		t.Fatal("duplicate active version must be rejected")
	}
}

func TestT14ManagerRebuildGivesFreshPending(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.7")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Advance(BuildFetching, 10); err != nil {
		t.Fatal(err)
	}
	if err := b.Advance(BuildFailed, 10); err != nil {
		t.Fatal(err)
	}
	nb, err := m.Start("6.18.7")
	if err != nil {
		t.Fatal(err)
	}
	if nb.State != BuildPending || nb.Progress != 0 {
		t.Fatalf("rebuild must be fresh pending/0, got %s/%d", nb.State, nb.Progress)
	}
	if nb == b {
		t.Fatal("rebuild must return a new build pointer")
	}
}

func TestT14VerifyConfigMissingList(t *testing.T) {
	missing := VerifyConfig("")
	if len(missing) != len(RequiredFamilies) {
		t.Fatalf("empty config must miss all %d families, got %d", len(RequiredFamilies), len(missing))
	}
	for i, fam := range RequiredFamilies {
		if missing[i] != fam {
			t.Fatalf("missing order must follow RequiredFamilies: index %d want %s got %s", i, fam, missing[i])
		}
	}
	// Drop exactly one family; the gap must name it.
	var body string
	for _, f := range RequiredFamilies[1:] {
		body += f + "=y\n"
	}
	missing = VerifyConfig(body)
	if len(missing) != 1 || missing[0] != RequiredFamilies[0] {
		t.Fatalf("want exactly [%s], got %v", RequiredFamilies[0], missing)
	}
	// Unknown keys never satisfy the check.
	if missing := VerifyConfig("CONFIG_NOT_REAL=y\n"); len(missing) != len(RequiredFamilies) {
		t.Fatalf("unknown keys must not satisfy families: %v", missing)
	}
}

func TestT14VerifyConfigModuleAccepted(t *testing.T) {
	var body string
	for _, f := range RequiredFamilies {
		body += f + "=m\n"
	}
	if missing := VerifyConfig(body); len(missing) != 0 {
		t.Fatalf("=m modules must satisfy policy, missing %v", missing)
	}
	// Commented lines do not count.
	commented := "# " + RequiredFamilies[0] + "=y\n"
	for _, f := range RequiredFamilies[1:] {
		commented += f + "=y\n"
	}
	missing := VerifyConfig(commented)
	if len(missing) != 1 || missing[0] != RequiredFamilies[0] {
		t.Fatalf("commented family must still be missing, got %v", missing)
	}
	// =n and bare names do not count either.
	negated := RequiredFamilies[0] + "=n\n"
	for _, f := range RequiredFamilies[1:] {
		negated += f + "=y\n"
	}
	if missing := VerifyConfig(negated); len(missing) != 1 {
		t.Fatalf("=n must not satisfy policy, got %v", missing)
	}
}

func TestT14TrainValidationEdges(t *testing.T) {
	if err := (Train{Version: "6.18.1", SourceURL: ""}).Validate(); err == nil {
		t.Fatal("empty source URL must fail")
	} else if !strings.Contains(err.Error(), "source URL") {
		t.Fatalf("wrong error for empty source: %v", err)
	}
	if err := (Train{Version: "", SourceURL: "https://x/kernel.tar.xz"}).Validate(); err == nil {
		t.Fatal("empty version must fail pinned-series check")
	}
	if err := (Train{Version: "6.19.0", SourceURL: "https://x/y"}).Validate(); err == nil {
		t.Fatal("off-series version must fail")
	}
	// Incompatible without rescan is fine (nothing trusted yet).
	if err := (Train{Version: "6.18.3", SourceURL: "https://x/y"}).Validate(); err != nil {
		t.Fatalf("incompatible entry without scan must pass: %v", err)
	}
	if err := (Train{Version: "6.18.3", SourceURL: "https://x/y", VirtioScanned: true}).Validate(); err != nil {
		t.Fatalf("scanned incompatible entry must pass: %v", err)
	}
}
