// Gateways, load balancers, port forwards, networks, firewall, cache, redirects.
package api

import (
	"fmt"
	"net/http"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Gateways / LB / Port forwards
// ============================================================================

func (a *API) handleListGateways(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /gateways")
}

func (a *API) handleCreateGateway(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /gateways")
}

func (a *API) handleGetGateway(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /gateways/{id}")
}

func (a *API) handlePatchGateway(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /gateways/{id}")
}

func (a *API) handleDeleteGateway(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /gateways/{id}")
}

func (a *API) handleListGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /gateways/{id}/routes")
}

func (a *API) handleCreateGatewayRoute(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /gateways/{id}/routes")
}

func (a *API) handleGetGatewayRoute(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /gateways/{id}/routes/{rid}")
}

func (a *API) handlePatchGatewayRoute(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /gateways/{id}/routes/{rid}")
}

func (a *API) handleDeleteGatewayRoute(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /gateways/{id}/routes/{rid}")
}

func (a *API) handleGatewayHealth(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /gateways/{id}/health")
}

func (a *API) handleListLoadBalancers(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /load-balancers")
}

func (a *API) handleCreateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /load-balancers")
}

func (a *API) handleGetLoadBalancer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /load-balancers/{id}")
}

func (a *API) handlePatchLoadBalancer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /load-balancers/{id}")
}

func (a *API) handleDeleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /load-balancers/{id}")
}

func (a *API) handleListPortForwards(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /port-forwards")
}

func (a *API) handleCreatePortForward(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /port-forwards")
}

func (a *API) handleDeletePortForward(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /port-forwards/{id}")
}

func (a *API) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListNetworks(a.projectID(r)))
}

func (a *API) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		CIDR   string `json:"cidr"`
		Driver string `json:"driver"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "network name is required")
		return
	}
	if req.CIDR == "" {
		req.CIDR = "10.42.0.0/24"
	}
	if req.Driver == "" {
		req.Driver = "bridge"
	}
	n := &types.Network{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, CIDR: req.CIDR, Driver: req.Driver, CreatedAt: time.Now()}
	a.store.PutNetwork(n)
	writeJSON(w, http.StatusCreated, n)
}

func (a *API) handleListRedirects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListRedirects(a.projectID(r)))
}

func (a *API) handleCreateRedirect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source    string `json:"source"`
		Target    string `json:"target"`
		Permanent bool   `json:"permanent"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	rd := &types.Redirect{ID: store.NewID(), ProjectID: a.projectID(r), Source: req.Source, Target: req.Target, Permanent: req.Permanent, CreatedAt: time.Now()}
	a.store.PutRedirect(rd)
	writeJSON(w, http.StatusCreated, rd)
}

func (a *API) handleDeleteRedirect(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteRedirect(r.PathValue("redirectId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "redirect not found")
}

func (a *API) handleBulkRedirects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Redirects []*types.Redirect `json:"redirects"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	for _, rd := range req.Redirects {
		if rd.ID == "" {
			rd.ID = store.NewID()
		}
		rd.ProjectID = a.projectID(r)
		a.store.PutRedirect(rd)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "count": len(req.Redirects)})
}

// ============================================================================
// Firewall / Cache / Volumes / Storage classes / Object stores / Backups
// ============================================================================

func (a *API) handleListFirewallRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListFirewallRules(a.projectID(r)))
}

func (a *API) handleCreateFirewallRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Direction string `json:"direction"`
		Action    string `json:"action"`
		Proto     string `json:"proto"`
		Ports     string `json:"ports"`
		Source    string `json:"source"`
		Priority  int    `json:"priority"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	fr := &types.FirewallRule{ID: store.NewID(), ProjectID: a.projectID(r), Direction: req.Direction, Action: req.Action, Proto: req.Proto, Ports: req.Ports, Source: req.Source, Priority: req.Priority, Active: true, CreatedAt: time.Now()}
	a.store.PutFirewallRule(fr)
	writeJSON(w, http.StatusCreated, fr)
}

func (a *API) handleGetFirewallRule(w http.ResponseWriter, r *http.Request) {
	if fr, ok := a.store.GetFirewallRule(r.PathValue("ruleId")); ok {
		writeJSON(w, http.StatusOK, fr)
		return
	}
	writeError(w, http.StatusNotFound, "firewall rule not found")
}

func (a *API) handlePatchFirewallRule(w http.ResponseWriter, r *http.Request) {
	fr, ok := a.store.GetFirewallRule(r.PathValue("ruleId"))
	if !ok {
		writeError(w, http.StatusNotFound, "firewall rule not found")
		return
	}
	var req struct {
		Action   string `json:"action"`
		Active   *bool  `json:"active"`
		Priority int    `json:"priority"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Action != "" {
		fr.Action = req.Action
	}
	if req.Active != nil {
		fr.Active = *req.Active
	}
	if req.Priority != 0 {
		fr.Priority = req.Priority
	}
	a.store.PutFirewallRule(fr)
	writeJSON(w, http.StatusOK, fr)
}

func (a *API) handleDeleteFirewallRule(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteFirewallRule(r.PathValue("ruleId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "firewall rule not found")
}

func (a *API) handleFirewallEvents(w http.ResponseWriter, r *http.Request) {
	events := a.store.ListHealthEvents(a.projectID(r), 50)
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "project_id": a.projectID(r)})
}

func (a *API) handleFirewallStats(w http.ResponseWriter, r *http.Request) {
	rules := a.store.ListFirewallRules(a.projectID(r))
	allowed, blocked, active := 0, 0, 0
	for _, fr := range rules {
		if fr.Active {
			active++
		}
		if fr.Action == "deny" {
			blocked++
		} else {
			allowed++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowed": allowed, "blocked": blocked, "active": active, "project_id": a.projectID(r)})
}

func (a *API) handleFirewallWhitelist(w http.ResponseWriter, r *http.Request) {
	rules := a.store.ListFirewallRules(a.projectID(r))
	whitelisted := []string{}
	for _, fr := range rules {
		if fr.Action == "allow" {
			whitelisted = append(whitelisted, fr.Source)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"whitelist": whitelisted, "project_id": a.projectID(r)})
}

func (a *API) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 300)
	hits, total := 0, len(tr)
	for _, e := range tr {
		if e.Status >= 300 && e.Status < 400 {
			hits++
		}
	}
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total) * 100
	}
	writeJSON(w, http.StatusOK, map[string]any{"hit_rate": rate, "entries": total, "hits": hits, "project_id": a.projectID(r)})
}

func (a *API) handleCachePurge(w http.ResponseWriter, r *http.Request) {
	if proj, ok := a.store.GetProject(a.projectID(r)); ok {
		a.store.ClearTrafficFor(proj.VMIDs)
	}
	a.store.AppendDaemonLog("cache purged for project " + a.projectID(r))
	if a.hub != nil {
		a.hub.Broadcast("cache.purged", map[string]any{"project_id": a.projectID(r)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "cache purged", "project_id": a.projectID(r)})
}

func (a *API) handleCachePurgePath(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	removed := a.store.ClearTrafficForPath(a.projectID(r), req.Path)
	a.store.AppendDaemonLog(fmt.Sprintf("cache path purge for project %s: %s (%d entries removed)", a.projectID(r), req.Path, removed))
	writeJSON(w, http.StatusOK, map[string]any{"status": "path purged", "path": req.Path, "project_id": a.projectID(r), "removed": removed})
}
