package incident

import "testing"

func TestLifecycle(t *testing.T) {
	in, err := New("i1", SeverityCritical, "gateway down")
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusMitigated, "op", "skip"); err == nil {
		t.Fatal("must ack first")
	}
	if err := in.Transition(StatusAcked, "op", "looking"); err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusMitigated, "op", "rerouted"); err != nil {
		t.Fatal(err)
	}
	if err := in.Transition(StatusResolved, "op", "done"); err == nil {
		t.Fatal("resolve needs remediation + postmortem")
	}
	in.Remediation = "failed over"
	in.Postmortem = "tunnel reaper race"
	if err := in.Transition(StatusResolved, "op", "closed"); err != nil {
		t.Fatal(err)
	}
	if len(in.Timeline) != 3 || !Page(SeverityCritical) || Page(SeverityLow) {
		t.Fatal("timeline/page broken")
	}
	if _, err := New("i2", "cosmic", "x"); err == nil {
		t.Fatal("bad severity must fail")
	}
}
