// Plans, subscriptions, products, prices, entitlements, invoices, payments, usage meters.
package api

import (
	"net/http"
	"time"

	"porter/internal/billing"
)

func (a *API) handleUsage(w http.ResponseWriter, r *http.Request) {
	period := usagePeriod(r)
	since := time.Now().Add(-period)
	var reqs, funcIn int64
	var bytesIn, bytesOut int64
	byDay := map[string]map[string]int64{}
	projects := map[string]map[string]int64{}
	for _, vm := range a.store.ListVMs() {
		if vm == nil {
			continue
		}
		for _, e := range a.store.ListTraffic(vm.ID, 2000) {
			if e.Timestamp.Before(since) {
				continue
			}
			reqs++
			bytesIn += e.BytesIn
			bytesOut += e.BytesOut
			if isFunctionPath(e.Path) {
				funcIn++
			}
			day := e.Timestamp.Format("2006-01-02")
			if byDay[day] == nil {
				byDay[day] = map[string]int64{}
			}
			byDay[day]["requests"]++
			byDay[day]["bandwidth"] += e.BytesIn + e.BytesOut
			pid := vm.ProjectID
			if projects[pid] == nil {
				projects[pid] = map[string]int64{}
			}
			projects[pid]["requests"]++
			projects[pid]["bandwidth"] += e.BytesIn + e.BytesOut
		}
	}
	days := make([]map[string]any, 0)
	for d := since.Truncate(24 * time.Hour); !d.After(time.Now()); d = d.Add(24 * time.Hour) {
		key := d.Format("2006-01-02")
		b := byDay[key]
		if b == nil {
			b = map[string]int64{}
		}
		days = append(days, map[string]any{
			"date": key, "requests": b["requests"], "bandwidth": b["bandwidth"],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"period": period.String(), "edge_requests": reqs,
		"function_invocations": funcIn, "fast_data_transfer": bytesIn + bytesOut,
		"data_transfer_in": bytesIn, "data_transfer_out": bytesOut,
		"projects": len(a.store.ListProjects()), "series": days, "by_project": projects,
	})
}

func (a *API) handleUsageBandwidth(w http.ResponseWriter, r *http.Request) {
	period := defaultPeriod(r)
	since := time.Now().Add(-period)
	var in, out int64
	for _, vm := range a.store.ListVMs() {
		for _, e := range a.store.ListTraffic(vm.ID, 2000) {
			if e.Timestamp.Before(since) {
				continue
			}
			in += e.BytesIn
			out += e.BytesOut
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bandwidth_bytes": in + out, "bytes_in": in, "bytes_out": out, "period": period,
	})
}

func (a *API) handleUsageRequests(w http.ResponseWriter, r *http.Request) {
	period := defaultPeriod(r)
	since := time.Now().Add(-period)
	var reqs, funcIn int64
	for _, vm := range a.store.ListVMs() {
		for _, e := range a.store.ListTraffic(vm.ID, 2000) {
			if e.Timestamp.Before(since) {
				continue
			}
			reqs++
			if isFunctionPath(e.Path) {
				funcIn++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"edge_requests": reqs, "function_invocations": funcIn, "period": period,
	})
}

func defaultPeriod(r *http.Request) time.Duration {
	switch r.URL.Query().Get("period") {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

func usagePeriod(r *http.Request) time.Duration { return defaultPeriod(r) }

func (a *API) handleListMeters(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /usage/meters")
}

func (a *API) handleListUsageEvents(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /usage/events")
}

// ============================================================================
// Billing / commerce
// ============================================================================

func (a *API) handleListPlans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"plans": a.store.ListPlans()})
}

func (a *API) handlePutPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID           string             `json:"id"`
		Name         string             `json:"name"`
		MonthlyCents int64              `json:"monthly_cents"`
		Prices       map[string]float64 `json:"prices"`
		Limits       map[string]int64   `json:"limits"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := a.store.PutPlan(billing.Plan{ID: req.ID, Name: req.Name, MonthlyCents: req.MonthlyCents}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for meter, cents := range req.Prices {
		if err := a.store.PutPrice(req.ID, meter, cents, "count"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	for key, val := range req.Limits {
		if err := a.store.SetPlanLimit(req.ID, key, val); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": req.ID})
}

func (a *API) handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": a.store.ListSubscriptions(a.projectID(r))})
}

func (a *API) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlanID string `json:"plan_id"`
	}
	if err := readJSON(r, &req); err != nil || req.PlanID == "" {
		writeError(w, http.StatusBadRequest, "plan_id is required")
		return
	}
	id, err := a.store.CreateSubscription(a.projectID(r), req.PlanID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "plan": req.PlanID})
}

func (a *API) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /projects/{projectId}/billing/subscription")
}

func (a *API) handleCancelSubscription(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /projects/{projectId}/billing/subscription")
}

func (a *API) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /subscriptions")
}

func (a *API) handleGetSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /subscriptions/{id}")
}

func (a *API) handleSuspendSubscription(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /subscriptions/{id}/suspend")
}

func (a *API) handleResumeSubscription(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /subscriptions/{id}/resume")
}

func (a *API) handleInvoicePreview(w http.ResponseWriter, r *http.Request) {
	projectID := a.projectID(r)
	planID, ok := a.store.ActiveSubscription(projectID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"total_cents": 0, "note": "no active subscription"})
		return
	}
	for _, p := range a.store.ListPlans() {
		if p.ID != planID {
			continue
		}
		start := time.Now().AddDate(0, -1, 0)
		inv := billing.Rate(p, projectID, start, a.store.MeterTotals(projectID, start))
		writeJSON(w, http.StatusOK, inv)
		return
	}
	writeError(w, http.StatusNotFound, "plan not found")
}

func (a *API) handleListProducts(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /products")
}

func (a *API) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /products")
}

func (a *API) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /products/{id}")
}

func (a *API) handlePatchProduct(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /products/{id}")
}

func (a *API) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /products/{id}")
}

func (a *API) handleListPrices(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /prices")
}

func (a *API) handleCreatePrice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /prices")
}

func (a *API) handlePatchPrice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /prices/{id}")
}

func (a *API) handleDeletePrice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /prices/{id}")
}

func (a *API) handleListEntitlements(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /entitlements")
}

func (a *API) handleCreateEntitlement(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /entitlements")
}

func (a *API) handleDeleteEntitlement(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /entitlements/{id}")
}

func (a *API) handleListInvoices(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /invoices")
}

func (a *API) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /invoices/{id}")
}

func (a *API) handleFinalizeInvoice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /invoices/{id}/finalize")
}

func (a *API) handlePayInvoice(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /invoices/{id}/pay")
}

func (a *API) handleListInvoiceLines(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /invoices/{id}/lines")
}

func (a *API) handleListPayments(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /payments")
}

func (a *API) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /payments/{id}")
}

func (a *API) handleRefundPayment(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /payments/{id}/refund")
}

func (a *API) handleListPaymentMethods(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /payment-methods")
}

func (a *API) handleCreatePaymentMethod(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /payment-methods")
}

func (a *API) handleDeletePaymentMethod(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /payment-methods/{id}")
}

func (a *API) handleListCredits(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /credits")
}

func (a *API) handleCreateCredit(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /credits")
}

func (a *API) handleListRefunds(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /refunds")
}

func (a *API) handleListTaxRates(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /tax-rates")
}

func (a *API) handleCreateTaxRate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /tax-rates")
}

func (a *API) handleListDunningPolicies(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dunning/policies")
}

func (a *API) handleCreateDunningPolicy(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /dunning/policies")
}

func (a *API) handleListDunningRuns(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dunning/runs")
}

func (a *API) handleNewSubscription(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"plans": a.store.ListPlans()})
}
