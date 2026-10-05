package triage

import (
	"context"
	"testing"

	"porter/internal/judge"
)

// stub is a deterministic Judger: canned answers, no network, no key.
type stub struct {
	choice judge.ChoiceA
	noul   float64
}

func (s stub) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{
		Choices: map[string]judge.ChoiceA{"failure_domain": s.choice},
		Nouls:   map[string]float64{"retryable": s.noul},
	}, nil
}

func TestInfraFlakeRetries(t *testing.T) {
	rep, err := TriageBuildFailure(context.Background(), stub{
		choice: judge.ChoiceA{Choice: DomainInfraFlake, Confidence: 0.9},
		noul:   0.85,
	}, "buildkitd: no space left on device")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != OutcomeRetry {
		t.Fatalf("want retry, got %s (%s)", rep.Outcome, rep.Reason)
	}
}

func TestDeterministicFails(t *testing.T) {
	rep, err := TriageBuildFailure(context.Background(), stub{
		choice: judge.ChoiceA{Choice: DomainAppCode, Confidence: 0.95},
		noul:   0.05,
	}, "go build: undefined: foo")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != OutcomeFail {
		t.Fatalf("want fail, got %s (%s)", rep.Outcome, rep.Reason)
	}
}

func TestUncertainEscalates(t *testing.T) {
	rep, err := TriageBuildFailure(context.Background(), stub{
		choice: judge.ChoiceA{Choice: DomainConfig, Confidence: 0.3},
		noul:   0.9,
	}, "???")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != OutcomeAttention {
		t.Fatalf("want needs_attention, got %s (%s)", rep.Outcome, rep.Reason)
	}
}

func TestEmptyLogErrors(t *testing.T) {
	if _, err := TriageBuildFailure(context.Background(), stub{}, ""); err == nil {
		t.Fatal("empty log must error, never produce a judgment")
	}
}
