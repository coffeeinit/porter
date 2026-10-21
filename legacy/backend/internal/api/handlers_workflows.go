// Workflows, operations, maintenance windows, crons.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"porter/internal/deployment"
	"porter/internal/store"
	"porter/internal/types"
)

func (a *API) handleListCrons(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListCrons(a.projectID(r)))
}

func (a *API) handleCreateCron(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Schedule string `json:"schedule"`
		JobImage string `json:"job_image"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	c := &types.Cron{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, Schedule: req.Schedule, JobImage: req.JobImage, Active: true, CreatedAt: time.Now()}
	a.store.PutCron(c)
	writeJSON(w, http.StatusCreated, c)
}

func (a *API) handleCronHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListCrons(a.projectID(r)))
}

func (a *API) handleGetCron(w http.ResponseWriter, r *http.Request) {
	if c, ok := a.store.GetCron(r.PathValue("cronId")); ok {
		writeJSON(w, http.StatusOK, c)
		return
	}
	writeError(w, http.StatusNotFound, "cron not found")
}

func (a *API) handlePatchCron(w http.ResponseWriter, r *http.Request) {
	c, ok := a.store.GetCron(r.PathValue("cronId"))
	if !ok {
		writeError(w, http.StatusNotFound, "cron not found")
		return
	}
	var req struct {
		Schedule string `json:"schedule"`
		Active   *bool  `json:"active"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Schedule != "" {
		c.Schedule = req.Schedule
	}
	if req.Active != nil {
		c.Active = *req.Active
	}
	a.store.PutCron(c)
	writeJSON(w, http.StatusOK, c)
}

func (a *API) handleDeleteCron(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteCron(r.PathValue("cronId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "cron not found")
}

func (a *API) handleRunCron(w http.ResponseWriter, r *http.Request) {
	c, ok := a.store.GetCron(r.PathValue("cronId"))
	if !ok {
		writeError(w, http.StatusNotFound, "cron not found")
		return
	}
	a.store.TouchCron(c.ID)
	a.store.AppendDaemonLog(fmt.Sprintf("cron %s triggered job %s", c.Name, c.JobImage))
	vm := &types.VM{
		ID: store.NewID(), Name: c.Name + "-job",
		ProjectID: c.ProjectID, ServiceName: "cron",
		State: types.StatePending, HealthStatus: types.HealthChecking,
		Image: c.JobImage, ReplicaIndex: -1, CreatedAt: time.Now(),
	}
	a.store.PutVM(vm)
	if a.vmm != nil {
		go func(v types.VM) { _ = a.vmm.Boot(context.Background(), &v) }(*vm)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "job booted as microVM", "cron": c.ID, "vm": vm.ID, "engine": "real"})
}

func (a *API) handleServerMaintenance(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /servers/{id}/maintenance")
}

// ============================================================================
// Workflows / webhooks / operations / maintenance
// ============================================================================

func (a *API) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /workflows")
}

func (a *API) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /workflows")
}

func (a *API) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /workflows/{id}")
}

func (a *API) handlePatchWorkflow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /workflows/{id}")
}

func (a *API) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /workflows/{id}")
}

func (a *API) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /workflows/{id}/run")
}

func (a *API) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /workflows/{id}/runs")
}

func (a *API) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /workflow-runs/{id}")
}

func (a *API) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /workflow-runs/{id}/cancel")
}

func (a *API) handleListOperations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"operations": a.store.ActiveOperations()})
}

func (a *API) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /operations/{id}")
}

func (a *API) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /operations/{id}/cancel")
}

func (a *API) handleListMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /maintenance-windows")
}

func (a *API) handleCreateMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /maintenance-windows")
}

func (a *API) handleGetMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /maintenance-windows/{id}")
}

func (a *API) handlePatchMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /maintenance-windows/{id}")
}

func (a *API) handleDeleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /maintenance-windows/{id}")
}

// recovered from internal/api/feature_more.go
func (a *API) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var job deployment.EphemeralJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		writeError(w, http.StatusBadRequest, "invalid job body")
		return
	}
	if err := job.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmdRaw, _ := json.Marshal(job.Command)
	opID, err := a.store.CreateOperationWithPayload("ephemeral", "job", job.Image, "",
		map[string]string{"cmd_json": string(cmdRaw)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record job")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"op_id": opID, "state": "QUEUED", "applied": false,
		"note": "intent recorded; task runner drives run → exec → destroy",
	})
}
