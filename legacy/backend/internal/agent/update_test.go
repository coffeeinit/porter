package agent

import (
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	w := Window{Days: []time.Weekday{time.Saturday}, StartHour: 2, EndHour: 6}
	sat := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC) // a Saturday
	if !w.Allowed(sat) {
		t.Fatal("inside window must allow")
	}
	if w.Allowed(sat.Add(4 * time.Hour)) {
		t.Fatal("outside hours must deny")
	}
	if w.Allowed(sat.AddDate(0, 0, 1)) {
		t.Fatal("wrong day must deny")
	}
	if err := (Window{}).Validate(); err == nil {
		t.Fatal("empty window must fail")
	}
	if err := Decide(UpdatePlan{NodeID: "n1", Current: "1.0", Desired: "1.1", Verified: true, InWindow: true}); err != nil {
		t.Fatal(err)
	}
	if err := Decide(UpdatePlan{NodeID: "n1", Current: "1.1", Desired: "1.1", Verified: true, InWindow: true}); err == nil {
		t.Fatal("no drift must deny")
	}
	if err := Decide(UpdatePlan{NodeID: "n1", Current: "1.0", Desired: "1.1", Verified: false, InWindow: true}); err == nil {
		t.Fatal("unverified must deny")
	}
}
