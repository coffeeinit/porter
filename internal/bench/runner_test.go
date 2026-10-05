package bench

import (
	"testing"
	"time"
)

func TestFromRunAndBudget(t *testing.T) {
	r := NewRun("alpine-128m", 128)
	base := r.Started
	for _, s := range []string{StageSerial, StageShell, StageAgent} {
		if err := r.Mark(s, base.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	r.RSSMiB = 40
	res, err := FromRun(r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shell != 2*time.Second || res.Agent != 2*time.Second {
		t.Fatalf("bad result %+v", res)
	}
	if err := (Budget{MaxShell: 10 * time.Second, MaxRSSMiB: 256}).Check(res); err != nil {
		t.Fatal(err)
	}
	if err := (Budget{MaxShell: time.Second}).Check(res); err == nil {
		t.Fatal("breach must fail")
	}
	empty := NewRun("x", 64)
	if _, err := FromRun(empty); err == nil {
		t.Fatal("stageless run must fail")
	}
}
