package ai

import "testing"

func TestClassify(t *testing.T) {
	r, err := Classify("get_logs")
	if err != nil || r != RiskRead {
		t.Fatalf("classify get_logs: %v %q", err, r)
	}
	r, err = Classify("restore_backup")
	if err != nil || r != RiskDestructive {
		t.Fatalf("classify restore_backup: %v %q", err, r)
	}
	if _, err := Classify("exec_shell"); err == nil {
		t.Fatal("arbitrary shell must never classify")
	}
}

func TestPlanValidate(t *testing.T) {
	p := Plan{Intent: "scale web", Risk: RiskProd, Steps: []string{"set replicas=3"},
		Verification: "health 3/3", Rollback: "set replicas=2", ApprovalBy: "op-1"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	noApproval := p
	noApproval.ApprovalBy = ""
	if err := noApproval.Validate(); err == nil {
		t.Fatal("prod plan without approver must fail")
	}
	empty := Plan{}
	if err := empty.Validate(); err == nil {
		t.Fatal("empty plan must fail")
	}
}
