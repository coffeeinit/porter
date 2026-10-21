package alertjudge

import (
	"context"
	"testing"

	"porter/internal/judge"
)

type stub struct {
	route judge.ChoiceA
	score judge.ScoreA
}

func (s stub) Evaluate(_ context.Context, _ any, _ map[string]judge.ChoiceQ, _ map[string]judge.NoulQ, _ map[string]judge.ScoreQ) (judge.Answers, error) {
	return judge.Answers{
		Choices: map[string]judge.ChoiceA{"route": s.route},
		Scores:  map[string]judge.ScoreA{"severity": s.score},
	}, nil
}

func TestCriticalPages(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		route: judge.ChoiceA{Choice: RoutePage, Confidence: 0.9},
		score: judge.ScoreA{Value: 2.9, Confidence: 0.9},
	}, Alert{Name: "vm-down", Summary: "all replicas failing health", Service: "api", Env: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Action != ActionPage {
		t.Fatalf("want page, got %s (%s)", rep.Action, rep.Reason)
	}
}

func TestShakyPageDowngrades(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		route: judge.ChoiceA{Choice: RoutePage, Confidence: 0.2},
		score: judge.ScoreA{Value: 2.9, Confidence: 0.2},
	}, Alert{Name: "flaky", Summary: "maybe bad", Service: "api", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Action != ActionNotify {
		t.Fatalf("want notify downgrade, got %s (%s)", rep.Action, rep.Reason)
	}
}

func TestInfoLogs(t *testing.T) {
	rep, err := Assess(context.Background(), stub{
		route: judge.ChoiceA{Choice: RouteLog, Confidence: 0.9},
		score: judge.ScoreA{Value: 0.2, Confidence: 0.9},
	}, Alert{Name: "deploy-done", Summary: "rollout finished", Service: "web", Env: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Action != ActionLog {
		t.Fatalf("want log, got %s (%s)", rep.Action, rep.Reason)
	}
}
