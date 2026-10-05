package riskgate

import (
	"context"
	"testing"

	"porter/internal/judge"
)

type stub struct {
	choice judge.ChoiceA
	score  judge.ScoreA
}

func (s stub) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{
		Choices: map[string]judge.ChoiceA{"change": s.choice},
		Scores:  map[string]judge.ScoreA{"risk": s.score},
	}, nil
}

func TestTrivialDevAuto(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		choice: judge.ChoiceA{Choice: ChangeConfigOnly, Confidence: 0.9},
		score:  judge.ScoreA{Value: 0.1, Confidence: 0.9},
	}, Deploy{Env: "dev", Replicas: 1, Summary: "flip feature flag off"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate != GateAuto {
		t.Fatalf("want auto, got %s (%s)", rep.Gate, rep.Reason)
	}
}

func TestProductionUpgradesToApproval(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		choice: judge.ChoiceA{Choice: ChangeRolling, Confidence: 0.9},
		score:  judge.ScoreA{Value: 0.8, Confidence: 0.8},
	}, Deploy{Env: "production", Replicas: 6, Summary: "bump api to v1.4.2"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate != GateExplicitApproval {
		t.Fatalf("want explicit_approval, got %s (%s)", rep.Gate, rep.Reason)
	}
}

func TestDangerousDenies(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		choice: judge.ChoiceA{Choice: ChangeInfra, Confidence: 0.9},
		score:  judge.ScoreA{Value: 2.8, Confidence: 0.9},
	}, Deploy{Env: "production", Replicas: 10, Summary: "reformat all volumes"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate != GateDeny {
		t.Fatalf("want deny, got %s (%s)", rep.Gate, rep.Reason)
	}
}

func TestUnknownDenies(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		choice: judge.ChoiceA{Choice: ChangeUnknown, Confidence: 0.9},
		score:  judge.ScoreA{Value: 0.2, Confidence: 0.9},
	}, Deploy{Env: "dev", Replicas: 1, Summary: "???"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate != GateDeny {
		t.Fatalf("want deny on unknown, got %s (%s)", rep.Gate, rep.Reason)
	}
}
