package cmdgate

import (
	"context"
	"errors"
	"testing"

	"porter/internal/judge"
)

// NOTE: cmdgate_test.go covers denylist-skip, read-allow, prod escalation,
// and low-confidence escalation; denied_table_a5_test.go covers DeniedInCode.
// These T12 cases cover the remaining Check branches via stub Judgers.

// t12Judger returns a fixed class/confidence without network.
type t12Judger struct {
	class string
	conf  float64
}

func (s t12Judger) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{Choices: map[string]judge.ChoiceA{
		"class": {Choice: s.class, Confidence: s.conf},
	}}, nil
}

// t12ErrJudger fails judgment to exercise error propagation.
type t12ErrJudger struct{ err error }

func (s t12ErrJudger) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{}, s.err
}

func TestT12CheckEmptyArgv(t *testing.T) {
	if _, err := Check(context.Background(), t12Judger{class: ActionRead, conf: 0.9}, "dev", nil); err == nil {
		t.Fatal("empty argv must fail")
	}
}

func TestT12CheckJudgmentDestructiveDenies(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionDestructive, conf: 0.9}, "dev", []string{"dropdb", "--if-exists", "production"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionDeny {
		t.Fatalf("judged destructive must deny, got %s (%s)", rep.Decision, rep.Reason)
	}
	if rep.Class != ActionDestructive || rep.Confidence != 0.9 {
		t.Fatalf("report must propagate class/confidence, got %+v", rep)
	}
}

func TestT12CheckProdChangeDevNeedsApproval(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionProdChange, conf: 0.9}, "dev", []string{"./migrate", "up"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("prod_change must need approval, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestT12CheckLowRiskDevLogs(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionLowRisk, conf: 0.9}, "dev", []string{"systemctl", "restart", "app"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionLog {
		t.Fatalf("low-risk in dev must allow with audit, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestT12CheckUnknownNeedsApproval(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionUnknown, conf: 0.9}, "dev", []string{"weird-binary"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("unknown class must need approval, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestT12CheckReadInProdNeedsApproval(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionRead, conf: 0.95}, "production", []string{"cat", "/etc/app.conf"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("read in production must need approval, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestT12CheckJudgerError(t *testing.T) {
	if _, err := Check(context.Background(), t12ErrJudger{err: errors.New("judge down")}, "dev", []string{"ls"}); err == nil {
		t.Fatal("judgment error must propagate")
	}
}

func TestT12CheckConfidenceBoundary(t *testing.T) {
	rep, err := Check(context.Background(), t12Judger{class: ActionRead, conf: 0.5}, "dev", []string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionAllow {
		t.Fatalf("confidence 0.5 is the pass threshold, got %s", rep.Decision)
	}
	rep, err = Check(context.Background(), t12Judger{class: ActionRead, conf: 0.49}, "dev", []string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("confidence below 0.5 must escalate, got %s", rep.Decision)
	}
}
