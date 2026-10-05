// Billing subscriptions + invoice preview (migration 0032): plans carry a
// flat monthly price plus per-meter unit prices; subscriptions bind projects
// to plans; Rating sums usage_events × prices. No money moves here —
// payment provider adapters land separately; invoices are previews.
package billing

import (
	"fmt"
	"time"
)

// Plan is one price book.
type Plan struct {
	ID           string
	Name         string
	MonthlyCents int64
	Prices       map[string]Price // by meter
}

// Price is one meter's unit rate.
type Price struct {
	UnitCents float64
	Unit      string
}

// Subscription binds a project to a plan.
type Subscription struct {
	ID        string
	ProjectID string
	PlanID    string
	State     string
	StartedAt time.Time
}

// Validate gates subscriptions.
func (s Subscription) Validate() error {
	if s.ProjectID == "" || s.PlanID == "" {
		return fmt.Errorf("billing: subscription needs project and plan")
	}
	switch s.State {
	case "", "active", "past_due", "cancelled":
	default:
		return fmt.Errorf("billing: unknown subscription state %q", s.State)
	}
	return nil
}

// InvoiceLine is one rated meter total.
type InvoiceLine struct {
	Meter    string
	Quantity float64
	Unit     string
	Cents    int64
}

// Invoice is a computed preview (not a charge).
type Invoice struct {
	ProjectID    string
	PlanID       string
	PeriodStart  time.Time
	MonthlyCents int64
	Lines        []InvoiceLine
	TotalCents   int64
}

// Rate computes the preview from meter totals. Unknown meters are listed
// at zero (visible, never silently dropped).
func Rate(plan Plan, projectID string, start time.Time, totals map[string]float64) Invoice {
	inv := Invoice{ProjectID: projectID, PlanID: plan.ID, PeriodStart: start, MonthlyCents: plan.MonthlyCents, TotalCents: plan.MonthlyCents}
	meters := make([]string, 0, len(totals))
	for m := range totals {
		meters = append(meters, m)
	}
	sortStrings(meters)
	for _, m := range meters {
		p := plan.Prices[m]
		line := InvoiceLine{Meter: m, Quantity: totals[m], Unit: p.Unit, Cents: int64(totals[m] * p.UnitCents)}
		if p.Unit == "" {
			line.Unit = "count"
		}
		inv.Lines = append(inv.Lines, line)
		inv.TotalCents += line.Cents
	}
	return inv
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
