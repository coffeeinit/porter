// Marketplace, extensions, templates, services, shares.
package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"porter/internal/compose"
	"porter/internal/gateway"
	"porter/internal/marketplace"
	"porter/internal/store"
	"porter/internal/types"
	"porter/internal/volumes"
)

func (a *API) handleGetService(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	pools := make([]map[string]any, 0)
	for name, pool := range proj.ServicePools {
		pools = append(pools, map[string]any{"name": name, "desired": pool.Desired, "healthy": pool.Healthy, "vms": pool.VMs})
	}
	if proj.ComposeYAML != "" {
		for _, svc := range a.serviceNames(proj) {
			if _, exists := proj.ServicePools[svc]; !exists {
				pools = append(pools, map[string]any{"name": svc, "desired": 1, "healthy": 0, "vms": []string{}})
			}
		}
	}
	name := r.PathValue("serviceName")
	for _, p := range pools {
		if p["name"] == name {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusOK, pools)
}

func (a *API) serviceNames(proj *types.Project) []string {
	if proj == nil || strings.TrimSpace(proj.ComposeYAML) == "" {
		return nil
	}
	svcs, err := compose.ParseCompose(proj.ComposeYAML)
	if err != nil {
		return nil
	}
	return svcNames(svcs)
}

// ============================================================================
// Services & Networks
// ============================================================================

func (a *API) handleListServices(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	pools := make([]map[string]any, 0)
	for name, pool := range proj.ServicePools {
		pools = append(pools, map[string]any{"name": name, "desired": pool.Desired, "healthy": pool.Healthy, "vms": pool.VMs})
	}
	if proj.ComposeYAML != "" {
		for _, svc := range a.serviceNames(proj) {
			if _, exists := proj.ServicePools[svc]; !exists {
				pools = append(pools, map[string]any{"name": svc, "desired": 1, "healthy": 0, "vms": []string{}})
			}
		}
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i]["name"].(string) < pools[j]["name"].(string) })
	writeJSON(w, http.StatusOK, pools)
}

func (a *API) handleScaleService(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "replicas must be >= 0")
		return
	}
	if floor := proj.ReplicaFloor(); req.Replicas < floor {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"project is a persistent vps workload: replicas cannot go below %d (convert back to microvm with DELETE /projects/{id}/vps first)", floor))
		return
	}
	cur := len(proj.VMIDs)
	spec := createProjectReq{Name: proj.Name, Image: proj.Image, Env: proj.Env, Ports: a.projPorts(proj), Replicas: 1}
	if req.Replicas > cur {
		for i := cur; i < req.Replicas; i++ {
			a.bootReplica(proj, spec, i)
		}
	} else if req.Replicas < cur {
		for i := cur - 1; i >= req.Replicas; i-- {
			if vm, vok := a.store.GetVM(proj.VMIDs[i]); vok {
				_ = a.vmm.Stop(context.Background(), vm)
			}
		}
		proj.VMIDs = proj.VMIDs[:req.Replicas]
	}
	proj.ReplicasDesired = req.Replicas
	proj.Replicas = req.Replicas
	a.store.PutProject(proj)
	a.store.AppendDaemonLog(fmt.Sprintf("service %s scaled to %d replica(s)", proj.Name, req.Replicas))
	writeJSON(w, http.StatusOK, map[string]any{"service": r.PathValue("serviceName"), "desired": req.Replicas, "current": len(proj.VMIDs), "status": "applied"})
}

// ============================================================================
// Extensions / marketplace
// ============================================================================

func (a *API) handleListExtensions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /extensions")
}

func (a *API) handleInstallExtension(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /extensions")
}

func (a *API) handleGetExtension(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /extensions/{id}")
}

func (a *API) handlePatchExtension(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /extensions/{id}")
}

func (a *API) handleUninstallExtension(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /extensions/{id}")
}

func (a *API) handleListMarketplaceItems(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /marketplace/items")
}

func (a *API) handleGetMarketplaceItem(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /marketplace/items/{id}")
}

func (a *API) handleInstallMarketplaceItem(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /marketplace/items/{id}/install")
}

// ============================================================================
// Templates / shares / email / Cloudflare
// ============================================================================

func (a *API) resolveTemplate(name string) (marketplace.ServiceTemplate, error) {
	return marketplace.ResolveTemplate(a.store, name)
}

func (a *API) handleListServiceTemplates(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	out := []marketplace.ServiceTemplate{}
	for _, row := range a.store.ListServiceTemplates() {
		seen[row.Name] = true
		out = append(out, marketplace.ServiceTemplate{
			Name: row.Name, Image: row.Image, Version: row.Version,
			Ports: row.Ports, VolumeMiB: row.VolumeMiB, Env: row.Env,
			Secrets: row.Secrets, Description: row.Description + " (custom)",
		})
	}
	for _, t := range marketplace.ServiceTemplates() {
		if !seen[t.Name] {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (a *API) handlePutServiceTemplate(w http.ResponseWriter, r *http.Request) {
	var row store.ServiceTemplateRow
	if err := readJSON(r, &row); err != nil || row.Name == "" || row.Image == "" {
		writeError(w, http.StatusBadRequest, "name and image are required")
		return
	}
	if err := a.store.PutServiceTemplate(row); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": row.Name})
}

func (a *API) handleDeleteServiceTemplate(w http.ResponseWriter, r *http.Request) {
	if !a.store.DeleteServiceTemplate(r.PathValue("name")) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleProvisionService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Template string `json:"template"`
		Name     string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || req.Template == "" {
		writeError(w, http.StatusBadRequest, "template is required")
		return
	}
	tmpl, err := a.resolveTemplate(req.Template)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	projectID := a.projectID(r)
	name := req.Name
	if name == "" {
		name = tmpl.Name
	}
	var volumeID string
	if a.volMgr != nil {
		vid := store.NewID()
		hostPath, verr := a.volMgr.Create(volumes.SanitizeID(vid), tmpl.VolumeMiB)
		if verr != nil {
			writeError(w, http.StatusInternalServerError, "provision volume: "+verr.Error())
			return
		}
		a.store.PutVolume(&types.Volume{ID: vid, ProjectID: projectID, Name: name + "-data", SizeMiB: tmpl.VolumeMiB, Path: hostPath, CreatedAt: time.Now()})
		volumeID = vid
	}
	gen := map[string]string{}
	for _, sname := range tmpl.Secrets {
		val := store.NewID() + store.NewID()
		if a.secretKeyMaterial == "" {
			writeError(w, http.StatusServiceUnavailable, "secret encryption not configured; set PORTER_SECRET_KEY")
			return
		}
		enc, encErr := a.encryptSecret(val)
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, "encrypt secret: "+encErr.Error())
			return
		}
		_ = a.store.PutSecret(&types.Secret{ID: store.NewID(), ProjectID: projectID, Name: sname, ValueEncrypted: enc, CreatedAt: time.Now()})
		gen[sname] = val
	}
	proj, ok := a.store.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if lim, over := a.quotaCheck(projectID, "max_vms", int64(len(proj.VMIDs)+1)); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_vms=%d (project has %d replicas)", lim, len(proj.VMIDs)))
		return
	}
	ports := []types.Port{}
	for _, p := range tmpl.Ports {
		ports = append(ports, types.Port{ContainerPort: p, HostPort: p})
	}
	fqdn := ""
	if a.baseDomain != "" && proj.Name != "" {
		fqdn = fmt.Sprintf("%s.%s.%s", name, proj.Name, a.baseDomain)
	}
	env := marketplace.ExpandPlaceholders(tmpl.Env, gen, fqdn)
	var hc *types.Healthcheck
	if tmpl.Healthcheck != nil {
		hc = &types.Healthcheck{Type: tmpl.Healthcheck.Type, Path: tmpl.Healthcheck.Path, Port: tmpl.Healthcheck.Port, IntervalSec: tmpl.Healthcheck.IntervalSec}
	}
	a.bootReplica(proj, createProjectReq{Name: name, Image: tmpl.Image, Replicas: 1, Env: env, Ports: ports, VolumeID: volumeID, Healthcheck: hc}, len(proj.VMIDs))
	schedID, _ := a.store.PutBackupSchedule(projectID, name, "0 2 * * *", 3)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name": name, "template": tmpl.Name, "image": tmpl.Image,
		"volume": volumeID, "ports": tmpl.Ports, "backup_schedule": schedID,
		"note": "boots via the replica path; needs staged image " + tmpl.Image,
	})
}

func (a *API) shareRegistry() *gateway.Registry {
	if a.shares == nil {
		a.shares = gateway.NewRegistry(nil)
	}
	return a.shares
}

func (a *API) handleShareOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Local string `json:"local"`
	}
	if err := readJSON(r, &req); err != nil || req.Local == "" {
		writeError(w, http.StatusBadRequest, "local address is required (e.g. localhost:8080)")
		return
	}
	s, err := a.shareRegistry().Open(r.Context(), store.NewID(), req.Local)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": s.ID, "url": s.URL, "local": s.Local})
}

func (a *API) handleShareList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"shares": a.shareRegistry().List()})
}

func (a *API) handleShareClose(w http.ResponseWriter, r *http.Request) {
	a.shareRegistry().Close(r.PathValue("shareId"))
	writeJSON(w, http.StatusOK, map[string]any{"status": "closed"})
}
