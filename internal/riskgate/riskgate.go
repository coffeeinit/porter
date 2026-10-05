// Package riskgate maps a planned deploy to the safety-model approval class
// (docs/security.md §6: READ auto / LOW-RISK policy / PROD approval /
// DESTRUCTIVE explicit) using TypeSafe judgments via judge.
//
// The model scores risk and classifies the change; code owns the gate:
// trivial → auto, routine → policy, risky → explicit approval, dangerous or
// uncertain → deny/escalate. Blast radius math (replica counts, env) stays in
// code and is part of the state, not the judgment.
package riskgate

import (
	"context"
	"fmt"

	"porter/internal/judge"
)

// Change classes the model may choose from. Unknown forces escalation.
const (
	ChangeConfigOnly = "config_only"
	ChangeRolling    = "code_rolling"
	ChangeMigration  = "migration"
	ChangeInfra      = "infra"
	ChangeUnknown    = "unknown"
)

// Gates are the only values callers switch on.
type Gate string

const (
	GateAuto             Gate = "auto"
	GatePolicy           Gate = "policy"
	GateExplicitApproval Gate = "explicit_approval"
	GateDeny             Gate = "deny"
)

// Deploy is the code-owned fact block: environment, scale, history.
type Deploy struct {
	Env            string // production | preview | dev
	Replicas       int
	Summary        string // human diff summary (commits, digest range)
	RecentFailures int    // failed deploys of this service in 24h
}

// Report is the composed gate decision.
type Report struct {
	Risk       float64 // 0 trivial … 3 dangerous (probability-weighted)
	Change     string
	Confidence float64
	Gate       Gate
	Reason     string
}

// Judger is satisfied by *judge.Client; tests stub it.
type Judger interface {
	Evaluate(ctx context.Context, state any, choices map[string]judge.ChoiceQ, nouls map[string]judge.NoulQ, scores map[string]judge.ScoreQ) (judge.Answers, error)
}

// Assess maps one deploy to its approval gate. Score + choice are independent
// (same state), so they are asked together in one request.
func Assess(ctx context.Context, j Judger, d Deploy) (Report, error) {
	if d.Env == "" || d.Summary == "" {
		return Report{}, fmt.Errorf("riskgate: env and summary are required")
	}
	state := map[string]any{
		"env": d.Env, "replicas": d.Replicas,
		"summary": d.Summary, "recent_failures": d.RecentFailures,
	}
	ans, err := j.Evaluate(ctx, state,
		map[string]judge.ChoiceQ{
			"change": {
				Instructions: "What kind of change is this deploy, judging only from `state.summary`?",
				Criteria: map[string]string{
					ChangeConfigOnly: "Only env vars, flags, or docs; no code or schema change",
					ChangeRolling:    "Code change safe to roll (stateless, backward compatible)",
					ChangeMigration:  "Schema/data migration or stateful change needing ordering",
					ChangeInfra:      "Infrastructure change (network, volumes, nodes, TLS, gateway)",
					ChangeUnknown:    "Cannot tell from the summary; do not force another option",
				},
			},
		},
		nil,
		map[string]judge.ScoreQ{
			"risk": {
				Instructions: "How risky is deploying this to `state.env` with `state.replicas` replicas and `state.recent_failures` recent failures?",
				Levels:       []string{"Trivial", "Routine", "Risky", "Dangerous"},
			},
		})
	if err != nil {
		return Report{}, fmt.Errorf("riskgate: judgment: %w", err)
	}
	change := ans.Choices["change"]
	risk := ans.Scores["risk"].Value
	rep := Report{Risk: risk, Change: change.Choice, Confidence: change.Confidence}

	// Code-owned policy. Production upgrades any gate one step; recent
	// failures upgrade risky→deny. Uncertainty denies: typed output is an
	// interface, not truth.
	base := GateAuto
	switch {
	case change.Choice == ChangeUnknown || change.Confidence < 0.5 || risk >= 2.5:
		base = GateDeny
	case risk >= 1.5 || change.Choice == ChangeMigration || change.Choice == ChangeInfra:
		base = GateExplicitApproval
	case risk >= 0.5 || change.Choice == ChangeRolling:
		base = GatePolicy
	}
	if d.Env == "production" && (base == GateAuto || base == GatePolicy) {
		base = GateExplicitApproval
		rep.Reason = "production requires explicit approval"
	}
	if d.RecentFailures > 0 && base == GateExplicitApproval {
		base = GateDeny
		rep.Reason = "recent failures; cool down before retry"
	}
	rep.Gate = base
	if rep.Reason == "" {
		rep.Reason = fmt.Sprintf("risk=%.2f change=%s", risk, change.Choice)
	}
	return rep, nil
}
