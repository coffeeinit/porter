package billing

import (
	"testing"
	"time"
)

func TestRateMonthlyBaseOnly(t *testing.T) {
	plan := Plan{ID: "pro", Name: "Pro", MonthlyCents: 1000}
	inv := Rate(plan, "proj-1", time.Now(), nil)
	if inv.TotalCents != 1000 {
		t.Fatalf("empty totals should yield monthly base only, got %d", inv.TotalCents)
	}
	if inv.MonthlyCents != 1000 || inv.PlanID != "pro" || inv.ProjectID != "proj-1" {
		t.Fatalf("invoice header wrong: %+v", inv)
	}
	if len(inv.Lines) != 0 {
		t.Fatalf("expected no lines, got %+v", inv.Lines)
	}
}

func TestRateMultipliesMeters(t *testing.T) {
	plan := Plan{
		ID:           "pro",
		MonthlyCents: 1000,
		Prices: map[string]Price{
			"vcpu_seconds":  {UnitCents: 0.01, Unit: "seconds"},
			"network_bytes": {UnitCents: 0.001, Unit: "bytes"},
		},
	}
	totals := map[string]float64{"vcpu_seconds": 1000, "network_bytes": 5000}
	inv := Rate(plan, "proj-1", time.Now(), totals)
	// 1000*0.01=10, 5000*0.001=5 → total 1000+10+5=1015.
	if inv.TotalCents != 1015 {
		t.Fatalf("expected total 1015, got %d (%+v)", inv.TotalCents, inv.Lines)
	}
	byMeter := map[string]InvoiceLine{}
	for _, l := range inv.Lines {
		byMeter[l.Meter] = l
	}
	if byMeter["vcpu_seconds"].Cents != 10 || byMeter["vcpu_seconds"].Quantity != 1000 {
		t.Fatalf("bad vcpu line: %+v", byMeter["vcpu_seconds"])
	}
	if byMeter["network_bytes"].Cents != 5 {
		t.Fatalf("bad network line: %+v", byMeter["network_bytes"])
	}
}

func TestRateUnknownMeterZeroVisible(t *testing.T) {
	plan := Plan{ID: "pro", MonthlyCents: 100, Prices: map[string]Price{}}
	inv := Rate(plan, "proj-1", time.Now(), map[string]float64{"mystery_meter": 42})
	if len(inv.Lines) != 1 {
		t.Fatalf("unknown meter must still be listed, got %+v", inv.Lines)
	}
	line := inv.Lines[0]
	if line.Meter != "mystery_meter" || line.Cents != 0 || line.Quantity != 42 {
		t.Fatalf("bad unknown-meter line: %+v", line)
	}
	if line.Unit != "count" {
		t.Fatalf("unknown meter unit should default to count, got %q", line.Unit)
	}
	if inv.TotalCents != 100 {
		t.Fatalf("unknown meters must not move the total, got %d", inv.TotalCents)
	}
}

func TestRateLinesSorted(t *testing.T) {
	plan := Plan{ID: "p", Prices: map[string]Price{
		"z_meter": {UnitCents: 1, Unit: "count"},
		"a_meter": {UnitCents: 1, Unit: "count"},
		"m_meter": {UnitCents: 1, Unit: "count"},
	}}
	inv := Rate(plan, "proj", time.Now(), map[string]float64{
		"z_meter": 1, "a_meter": 1, "m_meter": 1,
	})
	if len(inv.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %+v", inv.Lines)
	}
	want := []string{"a_meter", "m_meter", "z_meter"}
	for i, m := range want {
		if inv.Lines[i].Meter != m {
			t.Fatalf("lines not sorted: got %q at %d, want %q", inv.Lines[i].Meter, i, m)
		}
	}
}

func TestSubscriptionValidate(t *testing.T) {
	if err := (Subscription{ProjectID: "p", PlanID: "pro", State: "active"}).Validate(); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	if err := (Subscription{ProjectID: "p", PlanID: "pro"}).Validate(); err != nil {
		t.Fatalf("empty state should be valid (legacy rows): %v", err)
	}
	if err := (Subscription{PlanID: "pro"}).Validate(); err == nil {
		t.Fatal("missing project must fail")
	}
	if err := (Subscription{ProjectID: "p"}).Validate(); err == nil {
		t.Fatal("missing plan must fail")
	}
	if err := (Subscription{ProjectID: "p", PlanID: "pro", State: "weird"}).Validate(); err == nil {
		t.Fatal("unknown state must fail")
	}
}
