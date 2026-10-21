package bench

import (
	"testing"
	"time"
)

func TestRunOrder(t *testing.T) {
	r := NewRun("alpine-64m", 64)
	base := r.Started
	if err := r.Mark(StageShell, base.Add(time.Second)); err == nil {
		t.Fatal("skipping serial must fail")
	}
	for _, s := range []string{StageSerial, StageShell, StageAgent} {
		if err := r.Mark(s, base.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Mark(StageAgent, base.Add(2*time.Second)); err == nil {
		t.Fatal("repeat stage must fail")
	}
	sum := r.Summary()
	if sum[StageSerial] != time.Second || len(sum) != 3 {
		t.Fatalf("bad summary %v", sum)
	}
}

func TestP95(t *testing.T) {
	if P95(nil) != 0 {
		t.Fatal("empty = 0")
	}
	s := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	if got := P95(s); got != 19 {
		t.Fatalf("want 19, got %v", got)
	}
}
