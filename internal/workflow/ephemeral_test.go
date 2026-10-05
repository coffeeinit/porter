package workflow

import "testing"

func TestEphemeralAdvance(t *testing.T) {
	r := &EphemeralRun{JobID: "j1", Image: "alpine"}
	if err := r.Advance(nil); err != nil || r.Phase != EphemeralBooted {
		t.Fatalf("prepare->boot: %v", err)
	}
	if err := r.Advance(nil); err == nil {
		t.Fatal("boot->exec needs exit code")
	}
	code := 0
	if err := r.Advance(&code); err != nil || r.Phase != EphemeralExecuted {
		t.Fatalf("boot->exec: %v", err)
	}
	if err := r.Advance(nil); err != nil || !r.Destroyed() {
		t.Fatalf("exec->destroy: %v", err)
	}
	if err := r.Advance(nil); err == nil {
		t.Fatal("destroyed is terminal")
	}
}
