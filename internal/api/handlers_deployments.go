// Deployments, rollouts, checks, previews, compose, stacks, VPS, environments.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"porter/internal/compose"
	"porter/internal/imagecatalog"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Compose
// ============================================================================

func (a *API) handleGetCompose(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compose_yaml": proj.ComposeYAML})
}

func (a *API) handlePutCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComposeYAML string `json:"compose_yaml"`
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
	proj.ComposeYAML = req.ComposeYAML
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved"})
}

func (a *API) handleValidateCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComposeYAML string `json:"compose_yaml"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	svcs, err := compose.ParseCompose(req.ComposeYAML)
	if err != nil {
		writeError(w, http.StatusBadRequest, "compose parse error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "services": svcNames(svcs)})
}

func (a *API) handleComposePreview(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	names := []string{}
	if svcs, err := compose.ParseCompose(proj.ComposeYAML); err == nil {
		names = svcNames(svcs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": proj.ComposeYAML, "services": names})
}

func svcNames(svcs []compose.ComposeService) []string {
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return out
}

// ============================================================================
// Deployments & Rollout (incl. freeze + previews)
// ============================================================================

func (a *API) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, paginate(w, r, a.store.ListDeployments(a.projectID(r))))
}

func (a *API) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image     string            `json:"image"`
		Env       map[string]string `json:"env"`
		Tag       string            `json:"tag"`
		Commit    string            `json:"commit"`
		GitURL    string            `json:"git_url"`
		Rollout   string            `json:"rollout"`
		TrafficP  int               `json:"traffic_pct"`
		Replicas  int               `json:"replicas"`
		GuestBase string            `json:"guest_base"`
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
	if req.Rollout == "" {
		req.Rollout = "preview"
	}
	if req.Replicas < 1 {
		req.Replicas = proj.ReplicasDesired
		if req.Replicas < 1 {
			req.Replicas = 1
		}
	}
	if req.TrafficP < 0 || req.TrafficP > 100 {
		writeError(w, http.StatusBadRequest, "traffic_pct must be between 0 and 100")
		return
	}
	if req.Tag == "" {
		req.Tag = "rev-" + strings.ToLower(randHex(6))
	}
	defaultBase := "alpine"
	if a.hostConfig != nil && a.hostConfig.GuestBaseDefault != "" {
		defaultBase = a.hostConfig.GuestBaseDefault
	}
	base, baseErr := imagecatalog.ResolveGuestBase(req.GuestBase, defaultBase, nil)
	if baseErr != nil {
		writeError(w, http.StatusBadRequest, baseErr.Error())
		return
	}
	preview := ""
	if a.baseDomain != "" {
		preview = fmt.Sprintf("%s.%s.preview.%s", req.Tag, proj.Name, a.baseDomain)
	} else {
		preview = fmt.Sprintf("http://%s-%s.preview.local", req.Tag, proj.Name)
	}
	d := &types.Deployment{
		ID: store.NewID(), ProjectID: proj.ID,
		BuildStatus: "preview", ImageDigest: req.Image,
		Revision:     len(a.store.ListDeployments(proj.ID)) + 1,
		VersionLabel: req.Tag, GuestBase: base.Reference, Environment: "preview",
		RouteWeight: req.TrafficP, PreviewURL: previewHost(preview),
		RolloutPercent: req.TrafficP, RollbackTo: currentRollout(a.store.ListDeployments(proj.ID)),
		GitURL: req.GitURL, GitCommit: req.Commit,
		VMIDs: []string{}, CreatedAt: time.Now(),
	}
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	deploymentReq := createProjectReq{Name: proj.Name + "-" + req.Tag, Image: req.Image, Replicas: req.Replicas, Env: req.Env, Ports: a.projPorts(proj), deployment: d}
	if deploymentReq.Image == "" {
		deploymentReq.Image = proj.Image
	}
	if !a.knownDirectImage(deploymentReq.Image) {
		d.BuildStatus = "failed"
		_ = a.store.CreateDeployment(d)
		writeError(w, http.StatusUnprocessableEntity, "deployment image is not a registered direct Firecracker image")
		return
	}
	for i := 0; i < req.Replicas; i++ {
		a.bootReplica(proj, deploymentReq, i)
	}
	d.BuildStatus = "ready"
	_ = a.store.CreateDeployment(d)
	a.store.AddDomain(proj.ID, &types.Domain{ProjectID: proj.ID, Domain: previewHost(preview), Type: "preview", Status: "active"})
	a.store.AppendBuildLog(proj.ID, fmt.Sprintf("deployment %s (rev %d, %s) → preview at %s", req.Tag, d.Revision, deploymentReq.Image, preview))
	a.store.AppendDaemonLog(fmt.Sprintf("project %s deployment rev %d ready; preview %s", proj.Name, d.Revision, preview))
	if a.hub != nil {
		a.hub.Broadcast("deployment.created", map[string]any{"id": d.ID, "preview": preview, "image": req.Image})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": d, "preview_url": preview, "status": "preview"})
}

func currentRollout(ds []*types.Deployment) string {
	if len(ds) == 0 {
		return ""
	}
	return ds[len(ds)-1].ID
}

func (a *API) handleDeploymentUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if r.Method == http.MethodGet {
		req.Image = r.URL.Query().Get("image")
	} else if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "uploading", "image": req.Image})
}

func (a *API) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	for _, d := range a.store.ListDeployments(a.projectID(r)) {
		if d.ID == r.PathValue("deployId") {
			writeJSONETag(w, r, d)
			return
		}
	}
	writeError(w, http.StatusNotFound, "deployment not found")
}

func (a *API) handleDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.TailBuildLogs(a.projectID(r), 200)})
}

func (a *API) handleGetDeploymentChecks(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d, ok := a.store.GetDeployment(proj.ID, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment":  d.ID,
		"checks":      d.Checks,
		"all_passed":  store.AllChecksPassed(d),
		"rollout_pct": d.RolloutPercent,
	})
}

func (a *API) handleUpsertDeploymentChecks(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d, ok := a.store.GetDeployment(proj.ID, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	var req struct {
		Checks []types.DeploymentCheck `json:"checks"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	d.Checks = req.Checks
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment":  d.ID,
		"checks":      d.Checks,
		"all_passed":  store.AllChecksPassed(d),
		"rollout_pct": d.RolloutPercent,
	})
}

func (a *API) handleSetDeploymentCheck(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d, ok := a.store.GetDeployment(proj.ID, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	var req struct {
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	name := r.PathValue("checkName")
	updated := false
	for i := range d.Checks {
		if d.Checks[i].Name == name {
			d.Checks[i].Status = req.Status
			d.Checks[i].Detail = req.Detail
			updated = true
			break
		}
	}
	if !updated {
		writeError(w, http.StatusNotFound, "check not found: "+name)
		return
	}
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment": d.ID, "check": name, "status": req.Status,
		"all_passed": store.AllChecksPassed(d),
	})
}

func (a *API) handleSetDeploymentRollout(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d, ok := a.store.GetDeployment(proj.ID, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	var req struct {
		Percent int `json:"percent"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Percent < 0 || req.Percent > 100 {
		writeError(w, http.StatusBadRequest, "percent must be 0-100")
		return
	}
	d.RolloutPercent = req.Percent
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployment": d.ID, "rollout_pct": d.RolloutPercent})
}

func (a *API) handlePromoteDeployment(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	target := r.PathValue("deployId")
	var targetDep *types.Deployment
	for _, d := range a.store.ListDeployments(proj.ID) {
		if d.ID == target {
			targetDep = d
			break
		}
	}
	if targetDep == nil || targetDep.ImageDigest == "" {
		writeError(w, http.StatusNotFound, "deployment not found or has no image")
		return
	}
	if !store.AllChecksPassed(targetDep) {
		writeError(w, http.StatusConflict, "deployment has pending/failed checks; complete checks before promoting")
		return
	}
	keepOld := r.URL.Query().Get("keep_old") == "1"
	old := append([]string{}, proj.VMIDs...)
	proj.Image = targetDep.ImageDigest
	if targetDep.GitURL != "" && proj.Image == "" {
		proj.Image = targetDep.GitURL
	}
	if len(targetDep.VMIDs) == 0 {
		spec := createProjectReq{Name: proj.Name + "-" + targetDep.VersionLabel, Image: proj.Image, Replicas: proj.ReplicasDesired, Env: proj.Env, Ports: a.projPorts(proj), deployment: targetDep}
		for i := 0; i < proj.ReplicasDesired; i++ {
			a.bootReplica(proj, spec, i)
		}
	}
	proj.VMIDs = append([]string{}, targetDep.VMIDs...)
	for _, d := range a.store.ListDeployments(proj.ID) {
		d.IsProduction = d.ID == targetDep.ID
		if d.ID == targetDep.ID {
			d.Environment = "production"
			d.RouteWeight = 100
			d.RolloutPercent = 100
			d.BuildStatus = "live"
		} else if d.IsProduction {
			d.RouteWeight = 0
			d.IsProduction = false
		}
		_ = a.store.CreateDeployment(d)
	}
	if !keepOld {
		for _, vid := range old {
			if vm, vok := a.store.GetVM(vid); vok {
				_ = a.vmm.Stop(context.Background(), vm)
			}
		}
		for _, vid := range old {
			a.store.DeleteVM(vid)
		}
	}
	a.store.PutProject(proj)
	targetDep.BuildStatus = "live"
	targetDep.IsProduction = true
	targetDep.Environment = "production"
	targetDep.RouteWeight = 100
	targetDep.RolloutPercent = 100
	_ = a.store.CreateDeployment(targetDep)
	a.store.AppendDaemonLog(fmt.Sprintf("project %s promoted deployment %s (image %s); %d replica(s) live", proj.Name, targetDep.ID, proj.Image, len(proj.VMIDs)))
	if a.hub != nil {
		a.hub.Broadcast("deployment.promoted", map[string]any{"id": targetDep.ID, "image": proj.Image})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "promoted", "deployment": targetDep.ID, "preview": previewURL(targetDep.ID, proj.Name, a.baseDomain)})
}

func previewURL(id, project, baseDomain string) string {
	short := id
	if len(id) > 8 {
		short = id[:8]
	}
	if baseDomain != "" {
		return fmt.Sprintf("%s.%s.preview.%s", short, project, baseDomain)
	}
	return fmt.Sprintf("http://%s.%s.preview.local", short, project)
}

func (a *API) handleRollbackDeployment(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	deps := a.store.ListDeployments(proj.ID)
	target, ok := rollbackOf(deps, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "no rollback target for this deployment")
		return
	}
	proj.Image = target.ImageDigest
	proj.VMIDs = nil
	a.store.DeleteReplicasByProject(proj.ID)
	spec := createProjectReq{Name: proj.Name, Image: proj.Image, Replicas: proj.ReplicasDesired, Env: proj.Env, Ports: a.projPorts(proj)}
	for i := 0; i < proj.ReplicasDesired; i++ {
		a.bootReplica(proj, spec, i)
	}
	a.store.PutProject(proj)
	a.store.AppendDaemonLog(fmt.Sprintf("project %s rolled back to deployment %s", proj.Name, target.ID))
	writeJSON(w, http.StatusOK, map[string]any{"status": "rolled back", "deployment": target.ID})
}

func rollbackOf(deps []*types.Deployment, id string) (*types.Deployment, bool) {
	for i, d := range deps {
		if d.ID != id {
			continue
		}
		if d.RollbackTo != "" {
			for _, p := range deps {
				if p.ID == d.RollbackTo {
					return p, true
				}
			}
		}
		if i < len(deps)-1 {
			return deps[i+1], true
		}
		return nil, false
	}
	return nil, false
}

func (a *API) handleDeploymentSource(w http.ResponseWriter, r *http.Request) {
	d, ok := a.deploymentFor(r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	src := "image"
	if d.GitURL != "" {
		src = "git"
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployment": d.ID, "source": src, "git_url": d.GitURL, "commit": d.GitCommit})
}

func (a *API) handleDeploymentOG(w http.ResponseWriter, r *http.Request) {
	d, ok := a.deploymentFor(r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployment": d.ID, "image": d.ImageDigest, "status": d.BuildStatus, "git": d.GitURL})
}

func (a *API) deploymentFor(id string) (*types.Deployment, bool) {
	for _, p := range a.store.ListProjects() {
		for _, d := range a.store.ListDeployments(p.ID) {
			if d.ID == id {
				return d, true
			}
		}
	}
	return nil, false
}

func (a *API) handleListRollouts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListDeployments(a.projectID(r)))
}

// Deployment freeze (SRS §23)

func (a *API) handleFreezeDeployments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(r, &req)
	a.store.PutProjectSettings(a.projectID(r), "deployments", map[string]any{
		"frozen": true, "frozen_at": time.Now().Format(time.RFC3339), "reason": req.Reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{"frozen": true, "reason": req.Reason})
}

func (a *API) handleUnfreezeDeployments(w http.ResponseWriter, r *http.Request) {
	a.store.PutProjectSettings(a.projectID(r), "deployments", map[string]any{"frozen": false})
	writeJSON(w, http.StatusOK, map[string]any{"frozen": false})
}

func (a *API) handleFreezeStatus(w http.ResponseWriter, r *http.Request) {
	s := a.store.GetProjectSettings(a.projectID(r), "deployments")
	if s == nil {
		s = map[string]any{"frozen": false}
	}
	writeJSON(w, http.StatusOK, s)
}

// Previews (SRS §25)

func (a *API) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /projects/{projectId}/previews")
}

func (a *API) handleCreatePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
		TTL    int    `json:"ttl_hours"`
	}
	if err := decodeBody(r, &req); err != nil || req.Branch == "" {
		writeError(w, http.StatusBadRequest, "branch is required")
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	host := a.ensureBranchPreview(proj, req.Branch)
	if host == "" {
		writeError(w, http.StatusBadRequest, "cannot derive preview host")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"preview": host, "branch": req.Branch})
}

func (a *API) handleGetPreview(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /projects/{projectId}/previews/{pr}")
}

func (a *API) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /projects/{projectId}/previews/{pr}")
}

func (a *API) handleTeardownPreview(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	branch := r.PathValue("pr")
	removed := map[string]int{}
	removed["vms"] = a.deletePRPreviewVMRows(proj.ID, branch)
	removed["domains"] = a.deletePRPreviewDomains(proj, branch)
	removed["environments"] = a.deletePRPreviewEnvironments(proj, branch)
	writeJSON(w, http.StatusOK, map[string]any{"status": "torn_down", "removed": removed})
}

func (a *API) handlePreviewLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.TailLogs(r.PathValue("pr"), 100)})
}

// ============================================================================
// Environments
// ============================================================================

func (a *API) environmentForProject(r *http.Request) (*types.Environment, bool) {
	e, ok := a.store.GetEnvironment(r.PathValue("envId"))
	if !ok || e.ProjectID != a.projectID(r) {
		return nil, false
	}
	return e, true
}

func (a *API) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListEnvironments(a.projectID(r)))
}

func (a *API) handleCreateEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Branch    string `json:"branch"`
		URL       string `json:"url"`
		EnvDomain string `json:"env_domain"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name == "" {
		req.Name = "staging"
	}
	e := &types.Environment{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, Branch: req.Branch, URL: req.URL, EnvDomain: req.EnvDomain, CreatedAt: time.Now()}
	a.store.PutEnvironment(e)
	writeJSON(w, http.StatusCreated, e)
}

func (a *API) handleEnvironmentsAvailable(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	avail := []string{}
	for _, def := range []string{"production", "preview", "staging", "development"} {
		if !seen[def] {
			seen[def] = true
			avail = append(avail, def)
		}
	}
	active := a.store.ListEnvironments(a.projectID(r))
	for _, e := range active {
		if e.Name != "" && !seen[e.Name] {
			seen[e.Name] = true
			avail = append(avail, e.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": avail, "active": active})
}

func (a *API) handleGetEnvironment(w http.ResponseWriter, r *http.Request) {
	if e, ok := a.environmentForProject(r); ok {
		writeJSON(w, http.StatusOK, e)
		return
	}
	writeError(w, http.StatusNotFound, "environment not found")
}

func (a *API) handlePatchEnvironment(w http.ResponseWriter, r *http.Request) {
	e, ok := a.environmentForProject(r)
	if !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	var req struct {
		Branch    string `json:"branch"`
		URL       string `json:"url"`
		EnvDomain string `json:"env_domain"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Branch != "" {
		e.Branch = req.Branch
	}
	if req.URL != "" {
		e.URL = req.URL
	}
	if req.EnvDomain != "" {
		e.EnvDomain = req.EnvDomain
	}
	a.store.PutEnvironment(e)
	writeJSON(w, http.StatusOK, e)
}

func (a *API) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.environmentForProject(r); !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	if a.store.DeleteEnvironment(r.PathValue("envId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "environment not found")
}

func (a *API) handleEnvBranch(w http.ResponseWriter, r *http.Request) {
	e, ok := a.environmentForProject(r)
	if !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	e.Branch = req.Branch
	a.store.PutEnvironment(e)
	writeJSON(w, http.StatusOK, map[string]any{"environment": e.ID, "branch": e.Branch, "status": "updated"})
}

func (a *API) handleEnvDomain(w http.ResponseWriter, r *http.Request) {
	e, ok := a.environmentForProject(r)
	if !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	var req struct {
		EnvDomain string `json:"env_domain"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	e.EnvDomain = req.EnvDomain
	a.store.PutEnvironment(e)
	writeJSON(w, http.StatusOK, map[string]any{"environment": e.ID, "env_domain": e.EnvDomain, "status": "updated"})
}

func (a *API) handleEnvRange(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.environmentForProject(r); !ok {
		writeError(w, http.StatusNotFound, "environment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"environment": r.PathValue("envId"), "ranges": []string{"production", "preview"}})
}

// ============================================================================
// Stacks / VPS / kernel / bench
// ============================================================================

func (a *API) handleListStacks(w http.ResponseWriter, r *http.Request) {
	stacks := a.store.ListStacks()
	writeJSON(w, http.StatusOK, map[string]any{"stacks": paginate(w, r, stacks)})
}

func (a *API) handleGetStack(w http.ResponseWriter, r *http.Request) {
	st, ok := a.store.GetStack(r.PathValue("stackId"))
	if !ok {
		writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	members := make([]*types.Project, 0)
	for _, p := range a.store.ListProjects() {
		if p != nil && p.StackID == st.ID {
			members = append(members, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stack": st, "projects": members})
}

func (a *API) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("stackId")
	if _, ok := a.store.GetStack(id); !ok {
		writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	a.store.DeleteStack(id)
	a.store.AppendDaemonLog(fmt.Sprintf("stack %s deleted (member projects preserved)", id))
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

func (a *API) handleEnableVPS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StaticIP bool `json:"static_ip"`
	}
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	projectID := a.projectID(r)
	if _, ok := a.store.GetProject(projectID); !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err := a.store.SetProjectWorkloadType(projectID, types.WorkloadTypeVPS, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{"project_id": projectID, "workload_type": types.WorkloadTypeVPS, "persistent": true, "static_ip": req.StaticIP, "pinned": []string{}}
	if req.StaticIP {
		pinned := []string{}
		warnings := []string{}
		for _, vm := range a.store.ListReplicas(projectID) {
			if err := a.store.PinVMStaticIP(vm.ID, ""); err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			pinned = append(pinned, vm.ID)
		}
		resp["pinned"] = pinned
		resp["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) handleDisableVPS(w http.ResponseWriter, r *http.Request) {
	projectID := a.projectID(r)
	if _, ok := a.store.GetProject(projectID); !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	live := 0
	for _, vm := range a.store.ListReplicas(projectID) {
		if vmLive(vm) {
			live++
		}
	}
	if live > 0 && r.URL.Query().Get("force") != "true" {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"project has %d running VM(s); pass ?force=true to convert to microvm anyway", live))
		return
	}
	if err := a.store.SetProjectWorkloadType(projectID, types.WorkloadTypeMicroVM, false); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, vm := range a.store.ListReplicas(projectID) {
		_ = a.store.UnpinVMStaticIP(vm.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "workload_type": types.WorkloadTypeMicroVM, "persistent": false})
}

func (a *API) handleGetVPS(w http.ResponseWriter, r *http.Request) {
	projectID := a.projectID(r)
	wt, persistent, found := a.store.GetProjectWorkloadType(projectID)
	if !found {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	pinned := a.store.ListPinnedVMIPs(projectID)
	if pinned == nil {
		pinned = []store.PinnedIP{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID, "workload_type": wt, "persistent": persistent,
		"static_ip": len(pinned) > 0, "pinned": pinned,
	})
}

// recovered from internal/api/handlers.go
func (a *API) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	projectID := a.projectID(r)
	d, ok := a.store.GetDeployment(projectID, r.PathValue("deployId"))
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if d.IsProduction || d.Environment == "production" {
		writeError(w, http.StatusConflict, "cannot delete the production deployment; promote another version first")
		return
	}
	for _, vmID := range d.VMIDs {
		if vm, vok := a.store.GetVM(vmID); vok {
			if a.vmm != nil {
				if err := a.vmm.Stop(context.Background(), vm); err != nil {
					writeError(w, http.StatusConflict, "stop deployment VM "+vmID+": "+err.Error())
					return
				}
			}
			a.store.DeleteVM(vmID)
		}
	}
	if err := a.store.DeleteDeployment(projectID, d.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.store.DeleteDomain(projectID, previewHost(d.PreviewURL))
	a.store.AppendDaemonLog(fmt.Sprintf("deployment %s removed from project %s", d.VersionLabel, projectID))
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "deployment": d.ID, "tag": d.VersionLabel})
}
