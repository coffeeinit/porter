// Package triage turns failed build output into typed, actionable outcomes
// using TypeSafe System One judgments (skill: typesafe-ai) via judge.
//
// Split (per the skill: code owns the workflow, the model supplies
// programmable common sense): the model judges failure domain (choice) +
// retryability (noul) together over the same log state; code decides retry /
// needs_attention / fail. Low confidence always escalates to a human.
package triage

import (
	"context"
	"fmt"

	"porter/internal/judge"
)

// Failure domains. The model cannot choose an omitted value, so unknown
// failures must land on unknown, never be forced into another option.
const (
	DomainAppCode    = "app_code"
	DomainDependency = "dependency"
	DomainInfraFlake = "infra_flake"
	DomainConfig     = "config"
	DomainUnknown    = "unknown"
)

// Thresholds live in code so weight/display changes never rerun inference.
const (
	// RetryThreshold: retryable at/above this + transient domain = retry.
	RetryThreshold = 0.7
	// FailThreshold: retryable below this = fail without retry.
	FailThreshold = 0.35
	// MinDomainConfidence: below this the domain is untrusted → human.
	MinDomainConfidence = 0.5
)

// Outcomes are the only values callers switch on.
type Outcome string

const (
	OutcomeRetry     Outcome = "retry"
	OutcomeAttention Outcome = "needs_attention"
	OutcomeFail      Outcome = "fail"
)

// Judger is the narrow interface triage needs (judge.Client satisfies it;
// tests stub it with canned answers — no network, no key).
type Judger interface {
	Evaluate(ctx context.Context, state any, choices map[string]judge.ChoiceQ, nouls map[string]judge.NoulQ, scores map[string]judge.ScoreQ) (judge.Answers, error)
}

// Report is the composed, code-owned triage result.
type Report struct {
	Domain           string
	DomainConfidence float64
	Retryable        float64
	Outcome          Outcome
	Reason           string
}

// TriageBuildFailure classifies one failed build log.
func TriageBuildFailure(ctx context.Context, j Judger, logText string) (Report, error) {
	if logText == "" {
		return Report{}, fmt.Errorf("triage: empty build log")
	}
	ans, err := j.Evaluate(ctx,
		map[string]any{"build_log": logText},
		map[string]judge.ChoiceQ{
			"failure_domain": {
				Instructions: "What caused this build to fail, judging only from `state.build_log`?",
				Criteria: map[string]string{
					DomainAppCode:    "Code or test failure in the app itself (compile error, failing test, lint)",
					DomainDependency: "External fetch failed (registry, package install, git clone, network fetch)",
					DomainInfraFlake: "Runner/host transient (OOM-killed, disk full, daemon timeout, preempted)",
					DomainConfig:     "Build definition wrong (bad Dockerfile, unknown pack, missing file, bad flag)",
					DomainUnknown:    "None of the above fit; do not force another option",
				},
			},
		},
		map[string]judge.NoulQ{
			"retryable": {
				Instructions: "Would retrying this exact build without any change plausibly succeed, judging only from `state.build_log`?",
				TrueMeans:    "Transient/environmental failure a retry could pass",
				FalseMeans:   "Deterministic failure a retry would repeat",
			},
		},
		nil)
	if err != nil {
		return Report{}, fmt.Errorf("triage: judgment: %w", err)
	}

	domain := ans.Choices["failure_domain"]
	retryable := ans.Nouls["retryable"]
	rep := Report{Domain: domain.Choice, DomainConfidence: domain.Confidence, Retryable: retryable}

	// Composition is pure policy: uncertainty escalates; the middle band is
	// ambiguous by design rather than a gamble in either direction.
	switch {
	case domain.Choice == DomainUnknown || domain.Confidence < MinDomainConfidence:
		rep.Outcome = OutcomeAttention
		rep.Reason = "domain uncertain; human triage"
	case retryable >= RetryThreshold && (domain.Choice == DomainInfraFlake || domain.Choice == DomainDependency):
		rep.Outcome = OutcomeRetry
		rep.Reason = "transient domain with high retry probability"
	case retryable < FailThreshold:
		rep.Outcome = OutcomeFail
		rep.Reason = "deterministic failure; retry would repeat it"
	default:
		rep.Outcome = OutcomeAttention
		rep.Reason = "ambiguous; human triage"
	}
	return rep, nil
}
