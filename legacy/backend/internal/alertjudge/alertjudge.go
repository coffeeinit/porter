// Package alertjudge turns raw alerts into paged/notified/logged actions using
// TypeSafe judgments via judge.
//
// The model scores severity and routes; code owns the action with a hard rule:
// paging needs BOTH high severity and a page route (an "any serious violation"
// shape — never a weighted average). Low confidence downgrades one step so a
// shaky page becomes a notification, never silence.
package alertjudge

import (
	"context"
	"fmt"

	"porter/internal/judge"
)

// Routes the model may choose from.
const (
	RouteLog    = "log"
	RouteNotify = "notify"
	RoutePage   = "page"
)

// Actions are the only values callers switch on.
type Action string

const (
	ActionLog    Action = "log"
	ActionNotify Action = "notify"
	ActionPage   Action = "page"
)

// Alert is the code-owned fact block.
type Alert struct {
	Name    string
	Summary string
	Service string
	Env     string
}

// Report is the composed action.
type Report struct {
	Severity   float64 // 0 info … 3 critical (probability-weighted)
	Route      string
	Confidence float64
	Action     Action
	Reason     string
}

// Judger is satisfied by *judge.Client; tests stub it.
type Judger interface {
	Evaluate(ctx context.Context, state any, choices map[string]judge.ChoiceQ, nouls map[string]judge.NoulQ, scores map[string]judge.ScoreQ) (judge.Answers, error)
}

// Assess maps one alert to its action. Score + route are independent (same
// state), asked together in one request.
func Assess(ctx context.Context, j Judger, a Alert) (Report, error) {
	if a.Name == "" || a.Summary == "" {
		return Report{}, fmt.Errorf("alertjudge: name and summary are required")
	}
	state := map[string]any{"name": a.Name, "summary": a.Summary, "service": a.Service, "env": a.Env}
	ans, err := j.Evaluate(ctx, state,
		map[string]judge.ChoiceQ{
			"route": {
				Instructions: "Where should this alert go, judging from `state`?",
				Criteria: map[string]string{
					RouteLog:    "Informational; dashboard timeline only",
					RouteNotify: "Needs human eyes soon (channel notification)",
					RoutePage:   "Wake someone now (service down, data at risk)",
				},
			},
		}, nil,
		map[string]judge.ScoreQ{
			"severity": {
				Instructions: "How severe is this alert for `state.service` in `state.env`?",
				Levels:       []string{"Info", "Watch", "Urgent", "Critical"},
			},
		})
	if err != nil {
		return Report{}, fmt.Errorf("alertjudge: judgment: %w", err)
	}
	route := ans.Choices["route"]
	sev := ans.Scores["severity"].Value
	conf := (route.Confidence + ans.Scores["severity"].Confidence) / 2
	rep := Report{Severity: sev, Route: route.Choice, Confidence: conf}

	// Hard gate first: page = severe AND routed to page. Then downgrade one
	// step on shaky confidence. Weighted scores never page alone.
	switch {
	case sev >= 2.5 && route.Choice == RoutePage && conf >= 0.5:
		rep.Action, rep.Reason = ActionPage, "critical and page-routed with trusted judgments"
	case sev >= 2.5 && route.Choice == RoutePage:
		rep.Action, rep.Reason = ActionNotify, "page-worthy but uncertain; downgraded to notify"
	case sev >= 1.5 || route.Choice == RouteNotify:
		rep.Action, rep.Reason = ActionNotify, "needs human eyes"
	default:
		rep.Action, rep.Reason = ActionLog, "informational"
	}
	return rep, nil
}
