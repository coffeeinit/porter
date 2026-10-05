package workflow

import "testing"

func TestTransitions(t *testing.T) {
	mk := func() *Task { return &Task{ID: "t1", State: StateQueued} }
	k := mk()
	if err := k.Transition(StateSucceeded); err == nil {
		t.Fatal("QUEUED->SUCCEEDED must fail")
	}
	if err := k.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := k.Transition(StateSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := k.Transition(StateRunning); err == nil {
		t.Fatal("terminal must not reopen")
	}
	r := &Task{ID: "t2", State: StateFailed}
	if err := r.Transition(StateRunning); err == nil {
		t.Fatal("FAILED must go through RETRYING")
	}
	if err := r.Transition(StateRetrying); err != nil || r.Attempt != 1 {
		t.Fatalf("retry: %v attempt=%d", err, r.Attempt)
	}
	if err := r.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
}

func TestPercent(t *testing.T) {
	if (Progress{Total: 0}).Percent() != 0 {
		t.Fatal("zero total = 0%")
	}
	if got := (Progress{Total: 4, Done: 5}).Percent(); got != 100 {
		t.Fatalf("clamp 100, got %d", got)
	}
	if got := (Progress{Total: 200, Done: 50}).Percent(); got != 25 {
		t.Fatalf("want 25, got %d", got)
	}
}
