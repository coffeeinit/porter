// Billing persistence (migration 0032): plans, prices, subscriptions, and
// meter aggregation over usage_events for invoice previews.
package store

import (
	"context"
	"log"
	"time"

	"porter/internal/billing"
)

// PutPlan upserts a plan.
func (s *Store) PutPlan(p billing.Plan) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO billing_plans (id, name, monthly_cents)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, monthly_cents = EXCLUDED.monthly_cents`,
		p.ID, p.Name, p.MonthlyCents)
	if err != nil {
		log.Printf("store: put plan: %v", err)
	}
	return err
}

// ListPlans returns all plans with prices attached.
func (s *Store) ListPlans() []billing.Plan {
	if s.pool == nil {
		return nil
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, name, monthly_cents FROM billing_plans ORDER BY id`)
	if err != nil {
		log.Printf("store: list plans: %v", err)
		return nil
	}
	defer rows.Close()
	var out []billing.Plan
	for rows.Next() {
		var p billing.Plan
		if err := rows.Scan(&p.ID, &p.Name, &p.MonthlyCents); err != nil {
			continue
		}
		p.Prices = s.planPrices(p.ID)
		out = append(out, p)
	}
	return out
}

func (s *Store) planPrices(planID string) map[string]billing.Price {
	if s.pool == nil {
		return map[string]billing.Price{}
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT meter, unit_cents, unit FROM billing_prices WHERE plan_id = $1`, planID)
	if err != nil {
		return map[string]billing.Price{}
	}
	defer rows.Close()
	out := map[string]billing.Price{}
	for rows.Next() {
		var m string
		var p billing.Price
		if err := rows.Scan(&m, &p.UnitCents, &p.Unit); err != nil {
			continue
		}
		out[m] = p
	}
	return out
}

// PutPrice upserts one meter price.
func (s *Store) PutPrice(planID, meter string, unitCents float64, unit string) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO billing_prices (plan_id, meter, unit_cents, unit)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (plan_id, meter) DO UPDATE
		   SET unit_cents = EXCLUDED.unit_cents, unit = EXCLUDED.unit`,
		planID, meter, unitCents, unit)
	if err != nil {
		log.Printf("store: put price: %v", err)
	}
	return err
}

// CreateSubscription binds a project to a plan.
func (s *Store) CreateSubscription(projectID, planID string) (string, error) {
	if s.pool == nil {
		return "", nil
	}
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO billing_subscriptions (project_id, plan_id) VALUES ($1,$2)
		 RETURNING id::text`, projectID, planID).Scan(&id)
	if err != nil {
		log.Printf("store: create subscription: %v", err)
	}
	return id, err
}

// ListSubscriptions returns a project's subscriptions, newest first.
func (s *Store) ListSubscriptions(projectID string) []billing.Subscription {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id::text, project_id, plan_id, state, started_at FROM billing_subscriptions
		 WHERE project_id = $1 ORDER BY started_at DESC`, projectID)
	if err != nil {
		log.Printf("store: list subscriptions: %v", err)
		return nil
	}
	defer rows.Close()
	var out []billing.Subscription
	for rows.Next() {
		var sub billing.Subscription
		if err := rows.Scan(&sub.ID, &sub.ProjectID, &sub.PlanID, &sub.State, &sub.StartedAt); err != nil {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// MeterTotals sums usage per meter since a point in time (rating input).
func (s *Store) MeterTotals(projectID string, since time.Time) map[string]float64 {
	rows, err := s.pool.Query(context.Background(),
		`SELECT meter, SUM(quantity) FROM usage_events
		 WHERE project_id = $1 AND occurred_at >= $2 GROUP BY meter`, projectID, since)
	if err != nil {
		log.Printf("store: meter totals: %v", err)
		return map[string]float64{}
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var m string
		var q float64
		if err := rows.Scan(&m, &q); err != nil {
			continue
		}
		out[m] = q
	}
	return out
}
