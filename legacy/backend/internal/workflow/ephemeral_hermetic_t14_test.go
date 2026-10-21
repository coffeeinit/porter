package workflow

import "testing"

func TestT14ParseExitCodeEdges(t *testing.T) {
	for _, v := range []int{0, 1, 127, 255} {
		got, err := ParseExitCode(v)
		if err != nil || got != v {
			t.Fatalf("ParseExitCode(%d) = %d,%v; want %d,nil", v, got, err, v)
		}
	}
	for _, v := range []int{-1, -1000, 256, 1000, 1 << 30} {
		if _, err := ParseExitCode(v); err == nil {
			t.Fatalf("ParseExitCode(%d) must fail", v)
		}
	}
}

func TestT14EphemeralUnknownPhase(t *testing.T) {
	r := &EphemeralRun{JobID: "j-bogus", Image: "alpine", Phase: "bogus"}
	if err := r.Advance(nil); err == nil {
		t.Fatal("unknown phase must fail")
	}
	if r.Destroyed() {
		t.Fatal("unknown phase must not report destroyed")
	}
}

func TestT14EphemeralPreparedExplicit(t *testing.T) {
	r := &EphemeralRun{JobID: "j-prep", Image: "alpine", Phase: EphemeralPrepared}
	if err := r.Advance(nil); err != nil {
		t.Fatalf("prepared->booted: %v", err)
	}
	if r.Phase != EphemeralBooted {
		t.Fatalf("want booted, got %q", r.Phase)
	}
	if r.Destroyed() {
		t.Fatal("booted run must not be destroyed")
	}
}

func TestT14EphemeralExitStored(t *testing.T) {
	r := &EphemeralRun{JobID: "j-exit", Image: "alpine"}
	if err := r.Advance(nil); err != nil {
		t.Fatal(err)
	}
	code := 3
	if err := r.Advance(&code); err != nil {
		t.Fatal(err)
	}
	if r.Phase != EphemeralExecuted {
		t.Fatalf("want executed, got %q", r.Phase)
	}
	if r.Exit == nil || *r.Exit != 3 {
		t.Fatalf("exit pointer must retain 3, got %+v", r.Exit)
	}
	if r.Destroyed() {
		t.Fatal("executed run must not be destroyed yet")
	}
	if err := r.Advance(nil); err != nil {
		t.Fatal(err)
	}
	if !r.Destroyed() {
		t.Fatal("after final advance run must be destroyed")
	}
	// Exit survives destruction.
	if r.Exit == nil || *r.Exit != 3 {
		t.Fatalf("exit must survive destroy, got %+v", r.Exit)
	}
}

func TestT14EphemeralDestroyedTerminalStick(t *testing.T) {
	r := &EphemeralRun{JobID: "j-term", Image: "alpine", Phase: EphemeralDestroyed}
	if err := r.Advance(nil); err == nil {
		t.Fatal("destroyed must reject further advances")
	}
	code := 0
	if err := r.Advance(&code); err == nil {
		t.Fatal("destroyed must reject advances even with an exit code")
	}
	if !r.Destroyed() {
		t.Fatal("Destroyed() must stay true")
	}
}

func TestT14EphemeralBootedNeedsExitStrict(t *testing.T) {
	r := &EphemeralRun{JobID: "j-strict", Image: "alpine", Phase: EphemeralBooted}
	if err := r.Advance(nil); err == nil {
		t.Fatal("booted->executed without exit code must fail")
	}
	if r.Phase != EphemeralBooted {
		t.Fatalf("failed advance must not move phase, got %q", r.Phase)
	}
	if r.Exit != nil {
		t.Fatal("failed advance must not set exit")
	}
	// Boundary exit codes are accepted through the machine.
	for _, v := range []int{0, 255} {
		rr := &EphemeralRun{JobID: "j-b", Image: "alpine", Phase: EphemeralBooted}
		c := v
		if err := rr.Advance(&c); err != nil {
			t.Fatalf("exit %d must advance: %v", v, err)
		}
	}
}
