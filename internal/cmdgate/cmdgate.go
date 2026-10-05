// Package cmdgate gates guest exec commands into the safety-model classes
// (docs/security.md §6) using TypeSafe judgments via judge.
//
// Known-dangerous patterns are denied in CODE without inference (exact rules
// beat judgment). Everything else gets one choice judgment (read / low-risk /
// prod-change / destructive) composed into allow / log / require-approval /
// deny. Unknown or low-confidence always escalates.
package cmdgate

import (
	"context"
	"fmt"
	"strings"

	"porter/internal/judge"
)

// Action classes the model may choose from.
const (
	ActionRead        = "read"
	ActionLowRisk     = "low_risk"
	ActionProdChange  = "prod_change"
	ActionDestructive = "destructive"
	ActionUnknown     = "unknown"
)

// Decisions are the only values callers switch on.
type Decision string

const (
	DecisionAllow           Decision = "allow"
	DecisionLog             Decision = "allow_with_audit"
	DecisionRequireApproval Decision = "require_approval"
	DecisionDeny            Decision = "deny"
)

// Report is the composed gate decision (audited either way).
type Report struct {
	Class      string
	Confidence float64
	Decision   Decision
	Reason     string
}

// Judger is satisfied by *judge.Client; tests stub it.
type Judger interface {
	Evaluate(ctx context.Context, state any, choices map[string]judge.ChoiceQ, nouls map[string]judge.NoulQ, scores map[string]judge.ScoreQ) (judge.Answers, error)
}

// deniedInCode are exact-match destructive patterns: no inference spent.
// When several patterns match, the most specific (longest) one reports, so
// e.g. `rm -rf /*` is not shadowed by `rm -rf /`.
func deniedInCode(argv []string) (string, bool) {
	joined := strings.Join(argv, " ")
	lower := strings.ToLower(strings.TrimSpace(joined))
	if lower == "" {
		return "", false
	}
	// nospace collapses all whitespace so spaced-out variants
	// (`: () { :|:& } ; :`, `of = /dev/sda`) still match.
	nospace := strings.Join(strings.Fields(lower), "")

	type rule struct {
		pattern string
		match   bool
	}
	rules := []rule{
		{"rm -rf /*", strings.Contains(lower, "rm -rf /*")},
		{"rm -rf /", strings.Contains(lower, "rm -rf /")},
		{":(){:|:&};:", strings.Contains(nospace, ":(){:|:&};:")},
		{"mkfs.* /dev/", strings.Contains(lower, "mkfs") && strings.Contains(lower, "/dev/")},
		{"mkfs", strings.Contains(lower, "mkfs")},
		{"of=/dev/", strings.Contains(nospace, "of=/dev/")},
		{"chmod -R 777 /", chmod777Root(lower)},
		{"> /dev/", strings.Contains(lower, ">") && strings.Contains(lower, "/dev/")},
		{"wget|curl | sh", pipeToShell(lower)},
		{"shutdown", strings.Contains(lower, "shutdown")},
		{"reboot", strings.Contains(lower, "reboot")},
		{"halt", strings.Contains(lower, "halt")},
		{"poweroff", strings.Contains(lower, "poweroff")},
	}
	best := ""
	for _, r := range rules {
		if r.match && len(r.pattern) > len(best) {
			best = r.pattern
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// chmod777Root matches permission wipes aimed at the filesystem root
// (`chmod -R 777 /` and equivalents like `chmod 777 /etc/passwd`).
func chmod777Root(lower string) bool {
	if !strings.Contains(lower, "chmod") || !strings.Contains(lower, "777") {
		return false
	}
	return strings.Contains(lower, " /") || strings.HasSuffix(lower, "/") || strings.Contains(lower, "/*")
}

// pipeToShell matches fetch-and-execute (`wget ... | sh`, `curl ... | bash`).
// A bare `curl https://...` (no pipe) is left to judgment.
func pipeToShell(lower string) bool {
	if !strings.Contains(lower, "|") {
		return false
	}
	if !strings.Contains(lower, "wget") && !strings.Contains(lower, "curl") {
		return false
	}
	for _, seg := range strings.Split(lower, "|")[1:] {
		fields := strings.Fields(seg)
		for len(fields) > 0 && (fields[0] == "sudo" || fields[0] == "doas" || fields[0] == "env") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "sh", "bash", "dash", "zsh":
			return true
		}
	}
	return false
}

// DeniedInCode reports whether argv matches a destructive pattern without
// spending inference. Always safe to call (no key, no network); the exec
// path enforces it on every invocation and escalates to full judgment.
func DeniedInCode(argv []string) (string, bool) {
	return deniedInCode(argv)
}

// Check gates one exec invocation in a VM of the given environment.
func Check(ctx context.Context, j Judger, env string, argv []string) (Report, error) {
	if len(argv) == 0 {
		return Report{}, fmt.Errorf("cmdgate: no command given")
	}
	if pattern, bad := deniedInCode(argv); bad {
		return Report{Class: ActionDestructive, Confidence: 1, Decision: DecisionDeny,
			Reason: fmt.Sprintf("code denylist matched %q; no inference needed", pattern)}, nil
	}
	ans, err := j.Evaluate(ctx,
		map[string]any{"env": env, "argv": argv},
		map[string]judge.ChoiceQ{
			"class": {
				Instructions: "What safety class is running `state.argv` inside a `state.env` VM?",
				Criteria: map[string]string{
					ActionRead:        "Read-only inspection (cat, ls, ps, logs, status)",
					ActionLowRisk:     "Reversible, scoped change (restart app process, clear cache)",
					ActionProdChange:  "Production-affecting change (config rewrite, migration, scale)",
					ActionDestructive: "Irreversible or host-escaping (disk wipe, privilege change, exfiltration)",
					ActionUnknown:     "Cannot tell; do not force another option",
				},
			},
		}, nil, nil)
	if err != nil {
		return Report{}, fmt.Errorf("cmdgate: judgment: %w", err)
	}
	class := ans.Choices["class"]
	rep := Report{Class: class.Choice, Confidence: class.Confidence}
	switch {
	case class.Choice == ActionUnknown || class.Confidence < 0.5:
		rep.Decision, rep.Reason = DecisionRequireApproval, "class uncertain; approval required"
	case class.Choice == ActionDestructive:
		rep.Decision, rep.Reason = DecisionDeny, "destructive class denied"
	case class.Choice == ActionProdChange || env == "production":
		rep.Decision, rep.Reason = DecisionRequireApproval, "production-affecting; approval required"
	case class.Choice == ActionLowRisk:
		rep.Decision, rep.Reason = DecisionLog, "reversible; allowed with audit transcript"
	default:
		rep.Decision, rep.Reason = DecisionAllow, "read-only"
	}
	return rep, nil
}
