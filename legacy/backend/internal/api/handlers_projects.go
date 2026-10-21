// Projects: CRUD, scale, health, autoscale, status/logs/metrics.
package api

import (
	"context"
	"fmt"
	"net/http"

	"porter/internal/autoscale"
	"porter/internal/types"
)

func (a *API) projectID(r *http.Request) string { return r.PathValue("projectId") }

// ============================================================================
// Projects
// ============================================================================

func (a *API) handleListProjects(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	var projects []*types.Project
	if orgID != "" {
		projects = a.store.ListProjectsByOrg(orgID)
	}
	if projects == nil {
		projects = a.store.ListProjects()
	}
	if !a.projectListVisibleEverywhere(r) {
		projects = a.filterProjectsByScope(r, projects)
	}
	writeJSON(w, http.StatusOK, paginate(w, r, projects))
}

func (a *API) projectListVisibleEverywhere(r *http.Request) bool {
	me := currentUser(r)
	if a.store.HasCapability("user", me, "project.list", "platform", "") {
		return true
	}
	return a.store.RoleSeesAll(currentRole(r))
}

func (a *API) filterProjectsByScope(r *http.Request, projects []*types.Project) []*types.Project {
	me := currentUser(r)
	allowedOrgs := map[string]bool{}
	allowedProjects := map[string]bool{}
	for _, s := range a.store.UserScopes(me) {
		switch s.Type {
		case "org":
			allowedOrgs[s.ID] = true
		case "project":
			allowedProjects[s.ID] = true
		}
	}
	out := projects[:0]
	for _, p := range projects {
		if p == nil {
			continue
		}
		if allowedProjects[p.ID] || allowedOrgs[p.OrgID] {
			out = append(out, p)
		}
	}
	return out
}

func (a *API) handleGetProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if r.URL.Query().Get("expand") == "vms" {
		vms := []*types.VM{}
		for _, id := range proj.VMIDs {
			if vm, ok := a.store.GetVM(id); ok {
				vms = append(vms, vm)
			}
		}
		writeJSON(w, http.StatusOK, selectFields(r, map[string]any{"project": proj, "vms": vms}))
		return
	}
	writeJSONETag(w, r, selectFields(r, proj))
}

func (a *API) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if !checkIfMatch(w, r, proj) {
		return
	}
	var req struct {
		Tags            []string           `json:"tags"`
		HostMountPath   string             `json:"host_mount_path"`
		ReplicasDesired int                `json:"replicas_desired"`
		RestartPolicy   string             `json:"restart_policy"`
		Healthcheck     *types.Healthcheck `json:"healthcheck"`
		Env             map[string]string  `json:"env"`
		SSHEnabled      *bool              `json:"ssh_enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Tags != nil {
		proj.Tags = req.Tags
	}
	if req.HostMountPath != "" {
		proj.HostMountPath = req.HostMountPath
	}
	if req.ReplicasDesired >= 1 {
		proj.ReplicasDesired = req.ReplicasDesired
	}
	if req.RestartPolicy != "" {
		proj.RestartPolicy = req.RestartPolicy
	}
	if req.Healthcheck != nil {
		proj.Healthcheck = req.Healthcheck
	}
	if req.Env != nil {
		if proj.Env == nil {
			proj.Env = map[string]string{}
		}
		for k, v := range req.Env {
			proj.Env[k] = v
		}
	}
	if req.SSHEnabled != nil {
		proj.SSHEnabled = *req.SSHEnabled
	}
	a.store.PutProject(proj)
	if a.hub != nil {
		a.hub.Broadcast("project.updated", map[string]any{"project_id": proj.ID})
	}
	writeJSON(w, http.StatusOK, proj)
}

func (a *API) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := a.projectID(r)
	proj, ok := a.store.GetProject(id)
	if ok {
		for _, vid := range proj.VMIDs {
			if vm, vok := a.store.GetVM(vid); vok {
				_ = a.vmm.Delete(context.Background(), vm)
			}
		}
	}
	a.store.DeleteProject(id)
	a.store.AppendDaemonLog(fmt.Sprintf("project %s deleted", id))
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleRedeployProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	for _, vid := range proj.VMIDs {
		if vm, vok := a.store.GetVM(vid); vok {
			_ = a.vmm.Delete(context.Background(), vm)
		}
	}
	a.store.DeleteReplicasByProject(proj.ID)
	proj.VMIDs = nil
	for i := 0; i < proj.ReplicasDesired; i++ {
		a.bootReplica(proj, createProjectReq{Name: proj.Name, Image: proj.Image, Replicas: proj.ReplicasDesired, Env: proj.Env, Ports: a.projPorts(proj)}, i)
	}
	a.store.PutProject(proj)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "redeploying", "project": proj})
}

func (a *API) projPorts(proj *types.Project) []types.Port {
	var ports []types.Port
	for _, vid := range proj.VMIDs {
		if vm, ok := a.store.GetVM(vid); ok {
			ports = vm.Ports
			break
		}
	}
	return ports
}

func (a *API) handleRestartProject(w http.ResponseWriter, r *http.Request) {
	n, failures := a.mutateReplicas(a.projectID(r), nil, "restart")
	a.store.AppendDaemonLog(fmt.Sprintf("project %s restarted", a.projectID(r)))
	status := http.StatusOK
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"restarted": n, "failed": failures})
}

// ============================================================================
// Scale & Health
// ============================================================================

func (a *API) handleGetScale(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"desired": proj.ReplicasDesired, "current": len(proj.VMIDs), "project_id": proj.ID})
}

func (a *API) handleScale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replicas int `json:"replicas"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if req.Replicas < 0 {
		req.Replicas = 0
	}
	if floor := proj.ReplicaFloor(); req.Replicas < floor {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"project is a persistent vps workload: replicas cannot go below %d (convert back to microvm with DELETE /projects/{id}/vps first)", floor))
		return
	}
	if proj.Autoscale != nil && proj.Autoscale.Enabled {
		if req.Replicas < proj.Autoscale.MinReplicas {
			req.Replicas = proj.Autoscale.MinReplicas
		}
		if req.Replicas > proj.Autoscale.MaxReplicas {
			req.Replicas = proj.Autoscale.MaxReplicas
		}
	}
	if lim, over := a.quotaCheck(proj.ID, "max_vms", int64(req.Replicas)); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_vms=%d (requested %d)", lim, req.Replicas))
		return
	}
	cur := len(proj.VMIDs)
	if req.Replicas > cur {
		for i := cur; i < req.Replicas; i++ {
			a.bootReplica(proj, createProjectReq{Name: proj.Name, Image: proj.Image, Replicas: 1, Env: proj.Env, Ports: a.projPorts(proj)}, i)
		}
	} else if req.Replicas < cur {
		for i := cur - 1; i >= req.Replicas; i-- {
			if vm, ok := a.store.GetVM(proj.VMIDs[i]); ok {
				_ = a.vmm.Stop(context.Background(), vm)
			}
		}
		proj.VMIDs = proj.VMIDs[:req.Replicas]
	}
	proj.ReplicasDesired = req.Replicas
	a.store.PutProject(proj)
	if a.hub != nil {
		a.hub.Broadcast("pool.updated", map[string]any{"project_id": proj.ID, "desired": req.Replicas, "current": len(proj.VMIDs)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"desired": req.Replicas, "current": len(proj.VMIDs)})
}

func (a *API) handleGetHealthcheck(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if proj.Healthcheck == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, proj.Healthcheck)
}

func (a *API) handlePutHealthcheck(w http.ResponseWriter, r *http.Request) {
	var hc types.Healthcheck
	if err := readJSON(r, &hc); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if hc.Port <= 0 {
		hc.Port = 80
	}
	if hc.IntervalSec <= 0 {
		hc.IntervalSec = 30
	}
	proj.Healthcheck = &hc
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, proj.Healthcheck)
}

func (a *API) handleGetAutoscale(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if proj.Autoscale == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, proj.Autoscale)
}

func (a *API) handlePutAutoscale(w http.ResponseWriter, r *http.Request) {
	var p types.AutoscalePolicy
	if err := readJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if p.MaxReplicas <= 0 {
		p.MaxReplicas = 3
	}
	if p.MinReplicas < 0 {
		p.MinReplicas = 0
	}
	if p.TargetCPU <= 0 {
		p.TargetCPU = 80
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	proj.Autoscale = &p
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, proj.Autoscale)
}

func (a *API) notifyProject(projectID, subject, body string) {
	if a.mailer == nil {
		return
	}
	to := a.store.ProjectNotifyEmails(projectID)
	go func() {
		if err := a.mailer.Send(to, subject, "Project "+projectID+"\n\n"+body); err != nil {
			a.logger.Printf("notify: %v", err)
		}
	}()
}

// ============================================================================
// Analytics / observability / traces / audit
// ============================================================================

func (a *API) projectTraffic(projectID string, limit int) []*types.TrafficEntry {
	proj, ok := a.store.GetProject(projectID)
	if !ok {
		return nil
	}
	out := make([]*types.TrafficEntry, 0, 64)
	for _, vid := range proj.VMIDs {
		out = append(out, a.store.ListTraffic(vid, limit)...)
	}
	return out
}

func (a *API) handleTags(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /tags/{tagName?}")
}

// recovered from internal/api/handlers.go
func (a *API) handleExportProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	manifest := map[string]any{"project": proj.Name, "image": proj.Image, "replicas": proj.ReplicasDesired, "env": proj.Env, "source": proj.Source}
	writeJSON(w, http.StatusOK, map[string]any{"manifest": manifest})
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListHealthEvents(a.projectID(r), 50))
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectLiveness(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	healthy := 0
	for _, vid := range proj.VMIDs {
		if vm, ok := a.store.GetVM(vid); ok && vm.HealthStatus == types.HealthHealthy {
			healthy++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alive": healthy > 0, "healthy": healthy, "total": len(proj.VMIDs)})
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectLogStream(w http.ResponseWriter, r *http.Request) {
	projectID := a.projectID(r)
	if _, ok := a.store.GetProject(projectID); !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	serveLogStream(w, r, func() logStreamPayload {
		proj, found := a.store.GetProject(projectID)
		if !found {
			return logStreamPayload{Source: "project", Status: "missing"}
		}
		lines := make([]string, 0, 200)
		for _, vmID := range proj.VMIDs {
			lines = append(lines, a.store.TailLogs(vmID, 50)...)
		}
		lines = append(lines, a.store.TailBuildLogs(proj.ID, 50)...)
		return logStreamPayload{Source: "project", Lines: lines, Status: projStatus(proj)}
	}, nil)
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectLogs(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	logs := []string{}
	for _, vid := range proj.VMIDs {
		logs = append(logs, a.store.TailLogs(vid, 50)...)
	}
	logs = append(logs, a.store.TailBuildLogs(proj.ID, 50)...)
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "project_id": proj.ID})
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectMetrics(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	out := []*types.MetricSample{}
	for _, vid := range proj.VMIDs {
		out = append(out, a.store.ListMetrics(vid, 30)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectStatus(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": projStatus(proj), "desired": proj.ReplicasDesired, "current": len(proj.VMIDs)})
}

// recovered from internal/api/handlers.go
func (a *API) handleProjectTraffic(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	out := []*types.TrafficEntry{}
	for _, vid := range proj.VMIDs {
		out = append(out, a.store.ListTraffic(vid, 100)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// recovered from internal/api/handlers.go
func (a *API) handleTransferProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrgID string `json:"org_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.OrgID == "" {
		writeError(w, http.StatusBadRequest, "org_id is required")
		return
	}
	a.store.SetProjectOrg(a.projectID(r), req.OrgID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "transferred", "org_id": req.OrgID})
}

// from handlers.go
func projStatus(p *types.Project) string {
	switch {
	case len(p.VMIDs) == 0:
		return "pending"
	case p.Source == "compose":
		return "deploying"
	default:
		return "running"
	}
}

// --- project disruption budget (SRS §58): persisted as the
// autoscale.BudgetSection section of project_settings; all validation
// (unknown fields, non-integral, negative, both bounds) lives in
// autoscale.BudgetFromSettings.
func (a *API) handleGetDisruptionBudget(w http.ResponseWriter, r *http.Request) {
	data := a.store.GetProjectSettings(a.projectID(r), autoscale.BudgetSection)
	b, err := autoscale.BudgetFromSettings(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stored disruption budget invalid: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *API) handlePutDisruptionBudget(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	b, err := autoscale.BudgetFromSettings(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.store.PutProjectSettings(a.projectID(r), autoscale.BudgetSection, body)
	writeJSON(w, http.StatusOK, b)
}
