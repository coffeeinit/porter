package autoscale

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"porter/internal/resource"
)

func TestBudgetFromSettings(t *testing.T) {
	tests := []struct {
		name    string
		data    string // JSON as stored in project_settings.disruption_budget
		want    int
		wantMax int
		wantErr bool
	}{
		{"empty section ungates", `{}`, 0, 0, false},
		{"min available", `{"min_available":8}`, 8, 0, false},
		{"max unavailable", `{"max_unavailable":2}`, 0, 2, false},
		{"both set rejected", `{"min_available":8,"max_unavailable":2}`, 0, 0, true},
		{"negative rejected", `{"min_available":-1}`, 0, 0, true},
		{"unknown field rejected", `{"min_avilable":8}`, 0, 0, true},
		{"non-integer rejected", `{"min_available":"eight"}`, 0, 0, true},
		{"fractional rejected", `{"min_available":1.5}`, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal([]byte(tt.data), &data); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			b, err := BudgetFromSettings(data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BudgetFromSettings(%s) = %+v, %v; wantErr %v", tt.data, b, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if b.MinAvailable != tt.want || b.MaxUnavailable != tt.wantMax {
				t.Fatalf("BudgetFromSettings(%s) = %+v, want min %d max %d", tt.data, b, tt.want, tt.wantMax)
			}
		})
	}
}

func TestBudgetFromSettingsJSONNumber(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"max_unavailable":3}`))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	b, err := BudgetFromSettings(data)
	if err != nil {
		t.Fatalf("json.Number must parse: %v", err)
	}
	if b.MaxUnavailable != 3 {
		t.Fatalf("want max_unavailable 3, got %+v", b)
	}
}

func TestAllowance(t *testing.T) {
	tests := []struct {
		name string
		b    resource.DisruptionBudget
		pool Pool
		want int
	}{
		{"unset budget is unlimited", resource.DisruptionBudget{}, Pool{Desired: 10, Healthy: 10}, math.MaxInt},
		{"srs example allows two", resource.DisruptionBudget{MinAvailable: 8}, Pool{Desired: 10, Healthy: 10}, 2},
		{"in-flight evictions count", resource.DisruptionBudget{MinAvailable: 8}, Pool{Desired: 10, Healthy: 10, Evicting: 1}, 1},
		{"exhausted budget allows none", resource.DisruptionBudget{MinAvailable: 8}, Pool{Desired: 10, Healthy: 10, Evicting: 2}, 0},
		{"degraded pool allows none", resource.DisruptionBudget{MinAvailable: 8}, Pool{Desired: 10, Healthy: 7}, 0},
		{"max unavailable caps", resource.DisruptionBudget{MaxUnavailable: 3}, Pool{Desired: 10, Healthy: 10}, 3},
		{"allowance capped at pool size", resource.DisruptionBudget{MaxUnavailable: 99}, Pool{Desired: 4, Healthy: 4}, 4},
		{"zero pool allows none", resource.DisruptionBudget{MinAvailable: 1}, Pool{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allowance(tt.b, tt.pool); got != tt.want {
				t.Fatalf("Allowance(%+v, %+v) = %d, want %d", tt.b, tt.pool, got, tt.want)
			}
		})
	}
}

// Allowance must agree with repeated CanEvict calls: draining one at a time
// stops exactly at the allowance.
func TestAllowanceMatchesCanEvictSequence(t *testing.T) {
	b := resource.DisruptionBudget{MinAvailable: 8}
	pool := Pool{Desired: 10, Healthy: 10}
	n := Allowance(b, pool)
	for i := 0; i < n; i++ {
		if ok, reason := b.CanEvict(pool.Healthy, pool.Desired, pool.Evicting+i); !ok {
			t.Fatalf("CanEvict blocked within allowance at %d: %s", i, reason)
		}
	}
	if ok, reason := b.CanEvict(pool.Healthy, pool.Desired, pool.Evicting+n); ok {
		t.Fatalf("CanEvict must block at the allowance boundary (%d): %s", n, reason)
	}
}
