package cmdgate

import (
	"context"
	"testing"

	"porter/internal/judge"
)

type stub struct{ class judge.ChoiceA }

func (s stub) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{Choices: map[string]judge.ChoiceA{"class": s.class}}, nil
}

func TestDenylistSkipsInference(t *testing.T) {
	rep, err := Check(context.Background(), stub{}, "dev", []string{"rm", "-rf", "/"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionDeny {
		t.Fatalf("want deny, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestReadAllows(t *testing.T) {
	rep, err := Check(context.Background(), stub{
		class: judge.ChoiceA{Choice: ActionRead, Confidence: 0.95},
	}, "dev", []string{"ps", "aux"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionAllow {
		t.Fatalf("want allow, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestProdChangeNeedsApproval(t *testing.T) {
	rep, err := Check(context.Background(), stub{
		class: judge.ChoiceA{Choice: ActionLowRisk, Confidence: 0.9},
	}, "production", []string{"systemctl", "restart", "app"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("want require_approval on prod, got %s (%s)", rep.Decision, rep.Reason)
	}
}

func TestUncertainEscalates(t *testing.T) {
	rep, err := Check(context.Background(), stub{
		class: judge.ChoiceA{Choice: ActionRead, Confidence: 0.2},
	}, "dev", []string{"weird-binary", "--frobnicate"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decision != DecisionRequireApproval {
		t.Fatalf("want require_approval, got %s (%s)", rep.Decision, rep.Reason)
	}
}
