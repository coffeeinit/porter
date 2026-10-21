// T10 hermetic billing coverage: Rate math, Subscription states, and the
// invoice-preview shape. Pure functions only — no PG, no network, no server.
package billing

import (
	"testing"
	"time"
)

func TestT10RateMonthlyBase(t *testing.T) {
	plan := Plan{ID: "pro", Name: "Pro", MonthlyCents: 2500}
	inv := Rate(plan, "proj-1", time.Now(), nil)
	if inv.TotalCents != 2500 {
		t.Fatalf("nil totals should yield monthly base only, got %d", inv.TotalCents)
	}
	if inv.MonthlyCents != 2500 || inv.PlanID != "pro" || inv.ProjectID != "proj-1" {
		t.Fatalf("invoice header wrong: %+v", inv)
	}
	if len(inv.Lines) != 0 {
		t.Fatalf("expected no lines, got %+v", inv.Lines)
	}
}

func TestT10RateEmptyTotals(t *testing.T) {
	plan := Plan{ID: "pro", MonthlyCents: 750}
	for name, totals := range map[string]map[string]float64{
		"nil":   nil,
		"empty": {},
	} {
		inv := Rate(plan, "proj-1", time.Now(), totals)
		if inv.TotalCents != 750 {
			t.Fatalf("%s totals: expected base 750, got %d", name, inv.TotalCents)
		}
		if len(inv.Lines) != 0 {
			t.Fatalf("%s totals: expected no lines, got %+v", name, inv.Lines)
		}
	}
}

func TestT10RateMeterMultiplication(t *testing.T) {
	plan := Plan{
		ID:           "pro",
		MonthlyCents: 1000,
		Prices: map[string]Price{
			"vcpu_seconds":  {UnitCents: 0.02, Unit: "seconds"},
			"build_minutes": {UnitCents: 0.5, Unit: "minutes"},
		},
	}
	totals := map[string]float64{"vcpu_seconds": 2000, "build_minutes": 10}
	inv := Rate(plan, "proj-1", time.Now(), totals)
	// 2000*0.02=40, 10*0.5=5 → total 1000+40+5=1045.
	if inv.TotalCents != 1045 {
		t.Fatalf("expected total 1045, got %d (%+v)", inv.TotalCents, inv.Lines)
	}
	byMeter := map[string]InvoiceLine{}
	for _, l := range inv.Lines {
		byMeter[l.Meter] = l
	}
	vcpu := byMeter["vcpu_seconds"]
	if vcpu.Cents != 40 || vcpu.Quantity != 2000 || vcpu.Unit != "seconds" {
		t.Fatalf("bad vcpu line: %+v", vcpu)
	}
	build := byMeter["build_minutes"]
	if build.Cents != 5 || build.Quantity != 10 || build.Unit != "minutes" {
		t.Fatalf("bad build line: %+v", build)
	}
}

func TestT10RateUnknownMeterAtZero(t *testing.T) {
	plan := Plan{ID: "pro", MonthlyCents: 100, Prices: map[string]Price{}}
	inv := Rate(plan, "proj-1", time.Now(), map[string]float64{
		"future_meter":  7,
		"another_meter": 0,
	})
	if len(inv.Lines) != 2 {
		t.Fatalf("unknown meters must still be listed, got %+v", inv.Lines)
	}
	for _, l := range inv.Lines {
		if l.Cents != 0 {
			t.Fatalf("unknown meter %q must rate at zero, got %+v", l.Meter, l)
		}
		if l.Unit != "count" {
			t.Fatalf("unknown meter %q unit should default to count, got %q", l.Meter, l.Unit)
		}
	}
	if inv.TotalCents != 100 {
		t.Fatalf("unknown meters must not move the total, got %d", inv.TotalCents)
	}
}

func TestT10RateSortOrder(t *testing.T) {
	plan := Plan{ID: "p", Prices: map[string]Price{
		"mem_mib_seconds": {UnitCents: 1, Unit: "seconds"},
		"deployments":     {UnitCents: 1, Unit: "count"},
		"build_minutes":   {UnitCents: 1, Unit: "minutes"},
		"log_bytes":       {UnitCents: 1, Unit: "bytes"},
	}}
	inv := Rate(plan, "proj", time.Now(), map[string]float64{
		"mem_mib_seconds": 1, "deployments": 1, "build_minutes": 1, "log_bytes": 1,
	})
	want := []string{"build_minutes", "deployments", "log_bytes", "mem_mib_seconds"}
	if len(inv.Lines) != len(want) {
		t.Fatalf("expected %d lines, got %+v", len(want), inv.Lines)
	}
	for i, m := range want {
		if inv.Lines[i].Meter != m {
			t.Fatalf("lines not sorted: got %q at %d, want %q", inv.Lines[i].Meter, i, m)
		}
	}
}

func TestT10SubscriptionValidateStates(t *testing.T) {
	valid := []string{"", "active", "past_due", "cancelled"}
	for _, state := range valid {
		s := Subscription{ProjectID: "p", PlanID: "pro", State: state}
		if err := s.Validate(); err != nil {
			t.Errorf("state %q should be valid: %v", state, err)
		}
	}
	invalid := []string{"weird", "trialing", "suspended", "expired", "ACTIVE", " past_due"}
	for _, state := range invalid {
		s := Subscription{ProjectID: "p", PlanID: "pro", State: state}
		if err := s.Validate(); err == nil {
			t.Errorf("state %q must fail validation", state)
		}
	}
	if err := (Subscription{PlanID: "pro", State: "active"}).Validate(); err == nil {
		t.Error("missing project must fail")
	}
	if err := (Subscription{ProjectID: "p", State: "active"}).Validate(); err == nil {
		t.Error("missing plan must fail")
	}
	if err := (Subscription{State: "active"}).Validate(); err == nil {
		t.Error("missing project and plan must fail")
	}
}

// TestT10InvoicePreviewShape30d mirrors handleInvoicePreview: the trailing
// window fed into Rate, here with stub totals so the test stays hermetic.
// It pins the preview shape (header + recomputed total + sorted lines).
func TestT10InvoicePreviewShape30d(t *testing.T) {
	plan := Plan{
		ID:           "team",
		MonthlyCents: 1500,
		Prices: map[string]Price{
			"vcpu_seconds":  {UnitCents: 0.01, Unit: "seconds"},
			"network_bytes": {UnitCents: 0.001, Unit: "bytes"},
		},
	}
	start := time.Now().AddDate(0, -1, 0) // same window as handleInvoicePreview
	stubTotals := map[string]float64{
		"vcpu_seconds":  3000, // 30
		"network_bytes": 2000, // 2
		"mystery_meter": 9,    // 0, still listed
	}
	inv := Rate(plan, "proj-9", start, stubTotals)
	if inv.ProjectID != "proj-9" || inv.PlanID != "team" {
		t.Fatalf("preview header wrong: %+v", inv)
	}
	if !inv.PeriodStart.Equal(start) {
		t.Fatalf("period start not threaded through: %+v", inv)
	}
	if inv.MonthlyCents != 1500 {
		t.Fatalf("monthly base = %d, want 1500", inv.MonthlyCents)
	}
	// 1500 + 30 + 2 + 0 = 1532.
	if inv.TotalCents != 1532 {
		t.Fatalf("preview total = %d, want 1532 (%+v)", inv.TotalCents, inv.Lines)
	}
	// Total must reconcile as base + sum(lines).
	var sum int64
	for _, l := range inv.Lines {
		sum += l.Cents
	}
	if inv.TotalCents != inv.MonthlyCents+sum {
		t.Fatalf("total %d != base %d + lines %d", inv.TotalCents, inv.MonthlyCents, sum)
	}
	// All meters listed (unknown at zero), sorted by meter name.
	wantOrder := []string{"mystery_meter", "network_bytes", "vcpu_seconds"}
	if len(inv.Lines) != len(wantOrder) {
		t.Fatalf("expected %d lines, got %+v", len(wantOrder), inv.Lines)
	}
	for i, m := range wantOrder {
		if inv.Lines[i].Meter != m {
			t.Fatalf("line %d = %q, want %q (%+v)", i, inv.Lines[i].Meter, m, inv.Lines)
		}
	}
	if inv.Lines[0].Cents != 0 || inv.Lines[0].Unit != "count" {
		t.Fatalf("mystery meter should be zero/count, got %+v", inv.Lines[0])
	}
}
