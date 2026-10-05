// Managed env-block + exec-provider refs (OCM-16): service env is assembled
// as seed-minimal base plus one idempotent "# BEGIN PORTER MANAGED" block
// that atomic config-push rewrites wholesale. Secrets are referenced as
// exec:porter:NAME and resolved server-side at call time — values never
// live in the env file.
package secretbox

import (
	"fmt"
	"strings"
)

// ManagedBegin/End delimit the Porter-owned block inside an env file.
const (
	ManagedBegin = "# BEGIN PORTER MANAGED"
	ManagedEnd   = "# END PORTER MANAGED"
)

// ExecPrefix marks a server-side secret reference.
const ExecPrefix = "exec:porter:"

// ParseExecRef splits "exec:porter:NAME" into NAME. It reports false for
// anything else so callers treat plain values literally.
func ParseExecRef(v string) (string, bool) {
	name, ok := strings.CutPrefix(strings.TrimSpace(v), ExecPrefix)
	if !ok || name == "" || strings.ContainsAny(name, " \t\n=") {
		return "", false
	}
	return name, true
}

// MergeManagedBlock rewrites (or appends) the managed block in base,
// keeping everything outside the block byte-identical. Idempotent: merging
// the same vars twice yields the same file.
func MergeManagedBlock(base string, vars map[string]string) string {
	var body strings.Builder
	for _, kv := range sortVars(vars) {
		fmt.Fprintf(&body, "%s=%s\n", kv.k, quote(kv.v))
	}
	block := ManagedBegin + "\n" + body.String() + ManagedEnd + "\n"

	start := strings.Index(base, ManagedBegin)
	end := strings.Index(base, ManagedEnd)
	if start >= 0 && end > start {
		end += len(ManagedEnd)
		// Preserve one trailing newline convention.
		rest := strings.TrimPrefix(base[end:], "\n")
		head := base[:start]
		if head != "" && !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		return head + block + rest
	}
	trimmed := strings.TrimRight(base, "\n")
	if trimmed != "" {
		trimmed += "\n"
	}
	return trimmed + block
}

type kv struct{ k, v string }

func sortVars(vars map[string]string) []kv {
	out := make([]kv, 0, len(vars))
	for k, v := range vars {
		out = append(out, kv{k, v})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].k < out[j-1].k; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func quote(v string) string {
	if strings.ContainsAny(v, " \t\n\"'") {
		return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
	}
	return v
}
