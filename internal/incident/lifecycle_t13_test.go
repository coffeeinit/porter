package incident

import "testing"

func TestT13NewValidation(t *testing.T) {
	for _, sev := range []string{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow} {
		in, err := New("t13-"+sev, sev, "summary")
		if err != nil {
			t.Fatalf("severity %q must be accepted: %v", sev, err)
		}
		if in.Status != StatusOpen {
			t.Fatalf("new incident must open, got %q", in.Status)
		}
	}
	for name, args := range map[string][3]string{
		"bad severity":  {"i", "cosmic", "x"},
		"empty id":      {"", SeverityLow, "x"},
		"empty summary": {"i", SeverityLow, ""},
		"all empty":     {"", "", ""},
	} {
		if _, err := New(args[0], args[1], args[2]); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
}

func TestT13TransitionOrder(t *testing.T) {
	in, err := New("t13-order", SeverityHigh, "db slow")
	if err != nil {
		t.Fatal(err)
	}
	// No skipping, no going backwards, no resolving early.
	for _, next := range []string{StatusMitigated, StatusResolved, StatusOpen} {
		if err := in.Transition(next, "op", "skip"); err == nil {
			t.Fatalf("open -> %q must fail", next)
		}
	}
	if err := in.Transition(StatusAcked, "op", "looking"); err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusResolved, "op", "jump"); err == nil {
		t.Fatal("acked -> resolved must fail (mitigate first)")
	}
	if err := in.Transition(StatusAcked, "op", "again"); err == nil {
		t.Fatal("acked -> acked must fail")
	}
	if err := in.Transition(StatusMitigated, "op", "rerouted"); err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusAcked, "op", "back"); err == nil {
		t.Fatal("mitigated -> acked (backwards) must fail")
	}
}

func TestT13CloseGateVariants(t *testing.T) {
	mk := func() *Incident {
		in, err := New("t13-gate", SeverityMedium, "queue lag")
		if err != nil {
			t.Fatal(err)
		}
		if err := in.Transition(StatusAcked, "op", "a"); err != nil {
			t.Fatal(err)
		}
		if err := in.Transition(StatusMitigated, "op", "m"); err != nil {
			t.Fatal(err)
		}
		return in
	}
	// Neither note alone opens the gate.
	in := mk()
	if err := in.Transition(StatusResolved, "op", "done"); err == nil {
		t.Fatal("resolve with no notes must fail")
	}
	in.Remediation = "scaled workers"
	if err := in.Transition(StatusResolved, "op", "done"); err == nil {
		t.Fatal("resolve with remediation only must fail")
	}
	in.Remediation = ""
	in.Postmortem = "retry storm"
	if err := in.Transition(StatusResolved, "op", "done"); err == nil {
		t.Fatal("resolve with postmortem only must fail")
	}
	in.Remediation = "scaled workers"
	if err := in.Transition(StatusResolved, "op", "closed"); err != nil {
		t.Fatalf("resolve with both notes must pass: %v", err)
	}
	if in.Status != StatusResolved {
		t.Fatalf("status must be resolved, got %q", in.Status)
	}
	// Terminal: nothing leaves resolved.
	for _, next := range []string{StatusOpen, StatusAcked, StatusMitigated, StatusResolved} {
		if err := in.Transition(next, "op", "x"); err == nil {
			t.Fatalf("transition out of resolved to %q must fail", next)
		}
	}
}

func TestT13TimelineAndPage(t *testing.T) {
	in, err := New("t13-tl", SeverityLow, "disk 80%")
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusAcked, "alice", "paging?"); err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusMitigated, "bob", "cleaned"); err != nil {
		t.Fatal(err)
	}
	if len(in.Timeline) != 2 {
		t.Fatalf("want 2 timeline entries, got %d", len(in.Timeline))
	}
	if in.Timeline[0].Actor != "alice" || in.Timeline[0].Text != "paging?" {
		t.Fatalf("timeline must record actor+note, got %+v", in.Timeline[0])
	}
	if in.Timeline[1].Actor != "bob" {
		t.Fatalf("second entry actor wrong: %+v", in.Timeline[1])
	}
	if !Page(SeverityCritical) || !Page(SeverityHigh) {
		t.Fatal("critical/high must page")
	}
	if Page(SeverityMedium) || Page(SeverityLow) || Page("cosmic") || Page("") {
		t.Fatal("medium/low/unknown/empty must not page")
	}
}
