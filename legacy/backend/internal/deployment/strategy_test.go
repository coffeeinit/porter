package deployment

import "testing"

func TestRollbackOrder(t *testing.T) {
	r, err := NewRollback("d1", "v12")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Complete("shift_traffic"); err == nil {
		t.Fatal("out-of-order step must fail")
	}
	for _, s := range RollbackSteps {
		if err := r.Complete(s); err != nil {
			t.Fatal(err)
		}
	}
	if !r.Done() {
		t.Fatal("all steps done must report done")
	}
	if err := r.Complete("record_operation"); err == nil {
		t.Fatal("completed rollback must reject more steps")
	}
	if _, err := NewRollback("", "v1"); err == nil {
		t.Fatal("empty deployment must fail")
	}
}

func TestEphemeralJob(t *testing.T) {
	ok := EphemeralJob{Image: "alpine@sha256:abc", Command: []string{"echo", "hi"}, MemoryMiB: 128, Cores: 1}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (EphemeralJob{Command: []string{"x"}}).Validate(); err == nil {
		t.Fatal("missing image must fail")
	}
	if err := ValidateStrategy(StrategyCanary); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStrategy("teleport"); err == nil {
		t.Fatal("unknown strategy must fail")
	}
}
