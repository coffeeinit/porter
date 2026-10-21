// Disruption-budget evaluation for eviction gates (SRS 58). The budget value
// is persisted through the existing project_settings store (one JSON section
// per project: store.PutProjectSettings/GetProjectSettings); this file parses
// that payload and folds it with pool state into an eviction allowance. Pure
// policy — the caller owns all store I/O. The admin write path (API surface
// that puts the section) is planned.
package autoscale

import (
	"encoding/json"
	"fmt"
	"math"

	"porter/internal/resource"
)

// BudgetSection is the project_settings section carrying the workload
// disruption budget, serialized as {"min_available":N} or
// {"max_unavailable":N}.
const BudgetSection = "disruption_budget"

// BudgetFromSettings parses a GetProjectSettings payload into a budget.
// nil/empty yields the zero budget (no gate). Numbers may arrive as float64
// (default JSON decoding) or json.Number. Unknown fields and non-integral
// values are errors — a silently ignored typo would quietly change the
// availability contract.
func BudgetFromSettings(data map[string]any) (resource.DisruptionBudget, error) {
	if len(data) == 0 {
		return resource.DisruptionBudget{}, nil
	}
	var b resource.DisruptionBudget
	for k, v := range data {
		n, ok := settingInt(v)
		if !ok {
			return resource.DisruptionBudget{}, fmt.Errorf("disruption budget: %s must be an integer, got %T", k, v)
		}
		switch k {
		case "min_available":
			b.MinAvailable = n
		case "max_unavailable":
			b.MaxUnavailable = n
		default:
			return resource.DisruptionBudget{}, fmt.Errorf("disruption budget: unknown field %q", k)
		}
	}
	if err := b.Validate(); err != nil {
		return resource.DisruptionBudget{}, err
	}
	return b, nil
}

func settingInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

// Pool is one workload's availability snapshot for the eviction gate.
type Pool struct {
	Desired  int // replicas in the pool (persisted membership)
	Healthy  int // replicas reporting healthy
	Evicting int // evictions already in flight for this workload
}

// Allowance returns how many more evictions the budget permits right now.
// It is the closed form of asking CanEvict repeatedly, so the two can never
// disagree; the result is capped at Desired (there are only that many
// replicas to evict). An unset budget disables the gate: callers should
// check Set() first and skip the gate entirely; Allowance still answers
// unlimited so compositions stay correct.
func Allowance(b resource.DisruptionBudget, p Pool) int {
	if !b.Set() {
		return math.MaxInt
	}
	n := 0
	for n < p.Desired {
		ok, _ := b.CanEvict(p.Healthy, p.Desired, p.Evicting+n)
		if !ok {
			return n
		}
		n++
	}
	return n
}
