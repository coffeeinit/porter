// Alerts, incidents, SLOs, synthetics, traces, audit, analytics, web vitals, host overview.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"porter/internal/incident"
	"porter/internal/observability"
	"porter/internal/startup"
	"porter/internal/store"
	"porter/internal/types"
	"porter/internal/volumes"
)

func (a *API) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListAlerts(a.projectID(r)))
}

func (a *API) handleCreateAlert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string  `json:"name"`
		Metric    string  `json:"metric"`
		Threshold float64 `json:"threshold"`
		Op        string  `json:"op"`
		CooldownS int     `json:"cooldown_s"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	al := &types.Alert{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, Metric: req.Metric, Threshold: req.Threshold, Op: req.Op, CooldownS: req.CooldownS, CreatedAt: time.Now()}
	a.store.PutAlert(al)
	a.notifyProject(a.projectID(r), fmt.Sprintf("Alert created: %s (%s %v)", req.Name, req.Op, req.Threshold), "Metric-based alert registered for project "+a.projectID(r))
	writeJSON(w, http.StatusCreated, al)
}

func (a *API) handleGetAlert(w http.ResponseWriter, r *http.Request) {
	if al, ok := a.store.GetAlert(r.PathValue("alertId")); ok {
		writeJSON(w, http.StatusOK, al)
		return
	}
	writeError(w, http.StatusNotFound, "alert not found")
}

func (a *API) handlePatchAlert(w http.ResponseWriter, r *http.Request) {
	al, ok := a.store.GetAlert(r.PathValue("alertId"))
	if !ok {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	var req struct {
		Threshold float64 `json:"threshold"`
		Op        string  `json:"op"`
		Silenced  *bool   `json:"silenced"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Threshold != 0 {
		al.Threshold = req.Threshold
	}
	if req.Op != "" {
		al.Op = req.Op
	}
	if req.Silenced != nil {
		al.Silenced = *req.Silenced
	}
	a.store.PutAlert(al)
	writeJSON(w, http.StatusOK, al)
}

func (a *API) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteAlert(r.PathValue("alertId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "alert not found")
}

func (a *API) handleSilenceAlert(w http.ResponseWriter, r *http.Request) {
	a.store.SetAlertSilenced(r.PathValue("alertId"), true)
	writeJSON(w, http.StatusOK, map[string]any{"status": "silenced"})
}

func (a *API) handleUnsilenceAlert(w http.ResponseWriter, r *http.Request) {
	a.store.SetAlertSilenced(r.PathValue("alertId"), false)
	writeJSON(w, http.StatusOK, map[string]any{"status": "unsilenced"})
}

func (a *API) handleGetAlertRouting(w http.ResponseWriter, r *http.Request) {
	s := a.store.GetProjectSettings(a.orgIDFromHeader(r), "alert-routing")
	if s == nil {
		s = map[string]any{"routes": []any{}}
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *API) handlePutAlertRouting(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	a.store.PutProjectSettings(a.orgIDFromHeader(r), "alert-routing", req)
	writeJSON(w, http.StatusOK, req)
}

func (a *API) handleListSilences(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /alerts/silences")
}

func (a *API) handleCreateSilence(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /alerts/silences")
}

func (a *API) handleDeleteSilence(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /alerts/silences/{id}")
}

// ============================================================================
// Incidents / SLO / Synthetic
// ============================================================================

func (a *API) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"incidents": a.store.ListIncidents(a.projectID(r))})
}

func (a *API) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
	}
	if err := readJSON(r, &req); err != nil || req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if _, err := incident.New(store.NewID(), req.Severity, req.Summary); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := a.store.CreateIncident(a.projectID(r), req.Title, req.Severity, req.Summary)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (a *API) handleListAllIncidents(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /incidents")
}

func (a *API) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /incidents/{id}")
}

func (a *API) handlePatchIncident(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /incidents/{id}")
}

func (a *API) handleDeleteIncident(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /incidents/{id}")
}

func (a *API) handleAcknowledgeIncident(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /incidents/{id}/acknowledge")
}

func (a *API) handleResolveIncident(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /incidents/{id}/resolve")
}

func (a *API) handleIncidentPostmortem(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /incidents/{id}/postmortem")
}

func (a *API) handleIncidentTimeline(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /incidents/{id}/timeline")
}

func (a *API) handleAppendIncidentTimeline(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /incidents/{id}/timeline")
}

// SLOs

func (a *API) handleListSLOs(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /slos")
}

func (a *API) handleCreateSLO(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /slos")
}

func (a *API) handleGetSLO(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /slos/{id}")
}

func (a *API) handlePatchSLO(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /slos/{id}")
}

func (a *API) handleDeleteSLO(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /slos/{id}")
}

func (a *API) handleSLOBudget(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /slos/{id}/budget")
}

// Synthetic checks

func (a *API) handleListSyntheticChecks(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /synthetic-checks")
}

func (a *API) handleCreateSyntheticCheck(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /synthetic-checks")
}

func (a *API) handleGetSyntheticCheck(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /synthetic-checks/{id}")
}

func (a *API) handlePatchSyntheticCheck(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /synthetic-checks/{id}")
}

func (a *API) handleDeleteSyntheticCheck(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /synthetic-checks/{id}")
}

func (a *API) handleRunSyntheticCheck(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /synthetic-checks/{id}/run")
}

func (a *API) handleSyntheticResults(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /synthetic-checks/{id}/results")
}

func (a *API) handleAnalyticsUsage(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 200)
	var in, out int64
	for _, e := range tr {
		in += e.BytesIn
		out += e.BytesOut
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": a.projectID(r), "requests": len(tr),
		"bandwidth": in + out, "bytes_in": in, "bytes_out": out, "invocations": len(tr),
	})
}

func (a *API) handleAnalyticsTimeseries(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 500)
	buckets := map[string]int{}
	for _, e := range tr {
		buckets[e.Timestamp.Format("15:04")]++
	}
	series := make([]map[string]any, 0, len(buckets))
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		series = append(series, map[string]any{"t": k, "requests": buckets[k]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series, "project_id": a.projectID(r)})
}

func (a *API) handleAnalyticsPaths(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 200)
	paths := map[string]int{}
	for _, e := range tr {
		paths[e.Path]++
	}
	out := make([]map[string]any, 0, len(paths))
	for p, n := range paths {
		out = append(out, map[string]any{"path": p, "hits": n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["hits"].(int) > out[j]["hits"].(int) })
	writeJSON(w, http.StatusOK, map[string]any{"paths": out, "project_id": a.projectID(r)})
}

func (a *API) handleAnalyticsStatusCodes(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 500)
	codes := map[string]int{}
	for _, e := range tr {
		k := strconv.Itoa(e.Status)
		codes[k]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"status_codes": codes, "project_id": a.projectID(r)})
}

func (a *API) handleAnalyticsBandwidth(w http.ResponseWriter, r *http.Request) {
	var in, out int64
	for _, e := range a.projectTraffic(a.projectID(r), 500) {
		in += e.BytesIn
		out += e.BytesOut
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bandwidth_bytes": in + out, "bytes_in": in, "bytes_out": out, "project_id": a.projectID(r),
	})
}

func (a *API) handleAnalyticsRequests(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 500)
	writeJSON(w, http.StatusOK, map[string]any{"requests": len(tr), "project_id": a.projectID(r)})
}

func (a *API) handleAnalyticsInvocations(w http.ResponseWriter, r *http.Request) {
	tr := a.projectTraffic(a.projectID(r), 500)
	writeJSON(w, http.StatusOK, map[string]any{"invocations": len(tr), "project_id": a.projectID(r)})
}

func (a *API) handleWebVitalsBeacon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string             `json:"path"`
		Values map[string]float64 `json:"values"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	projID := a.projectID(r)
	now := time.Now()
	for metric, value := range req.Values {
		if value < 0 {
			continue
		}
		a.store.AddVital(&types.WebVital{
			ProjectID: projID, Path: req.Path, Metric: metric,
			Value: value, Rating: vitalRating(metric, value), Timestamp: now,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "recorded", "count": len(req.Values)})
}

func vitalRating(metric string, v float64) string {
	switch metric {
	case "lcp_ms":
		if v <= 2500 {
			return "good"
		}
		if v <= 4000 {
			return "needs-improvement"
		}
		return "poor"
	case "cls":
		if v <= 0.1 {
			return "good"
		}
		if v <= 0.25 {
			return "needs-improvement"
		}
		return "poor"
	case "inp_ms":
		if v <= 200 {
			return "good"
		}
		if v <= 500 {
			return "needs-improvement"
		}
		return "poor"
	default:
		if v <= 800 {
			return "good"
		}
		if v <= 1800 {
			return "needs-improvement"
		}
		return "poor"
	}
}

func (a *API) handleWebVitals(w http.ResponseWriter, r *http.Request) {
	vs := a.store.ListVitals(a.projectID(r), 200)
	byMetric := map[string][]float64{}
	for _, v := range vs {
		byMetric[v.Metric] = append(byMetric[v.Metric], v.Value)
	}
	out := make([]map[string]any, 0, len(byMetric))
	for m, vals := range byMetric {
		if len(vals) == 0 {
			continue
		}
		sort.Float64s(vals)
		p75 := vals[(len(vals)-1)*3/4]
		good := 0
		for _, v := range vals {
			if vitalRating(m, v) == "good" {
				good++
			}
		}
		out = append(out, map[string]any{
			"metric": m, "p75": p75, "count": len(vals),
			"good": good, "percent": float64(good*100) / float64(len(vals)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["count"].(int) > out[j]["count"].(int) })
	writeJSON(w, http.StatusOK, map[string]any{"web_vitals": out, "project_id": a.projectID(r)})
}

func (a *API) handleWebVitalsTimeseries(w http.ResponseWriter, r *http.Request) {
	vs := a.store.ListVitals(a.projectID(r), 100)
	series := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		series = append(series, map[string]any{"t": v.Timestamp.Format("15:04"), "metric": v.Metric, "value": v.Value})
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series, "project_id": a.projectID(r)})
}

func (a *API) handleGlobalAnalytics(w http.ResponseWriter, r *http.Request) {
	reqs := 0
	for _, vm := range a.store.ListVMs() {
		reqs += len(a.store.ListTraffic(vm.ID, 100))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": reqs, "projects": len(a.store.ListProjects())})
}

func (a *API) handleGlobalAnalyticsTimeseries(w http.ResponseWriter, r *http.Request) {
	buckets := map[string]int{}
	for _, vm := range a.store.ListVMs() {
		for _, e := range a.store.ListTraffic(vm.ID, 30) {
			buckets[e.Timestamp.Format("15:04")]++
		}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	series := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		series = append(series, map[string]any{"t": k, "requests": buckets[k]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series})
}

func isFunctionPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/functions/") || strings.Contains(p, "/fn/")
}

func (a *API) handleListTraces(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /traces")
}

func (a *API) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /traces/{id}")
}

func (a *API) handleSearchTraces(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /traces/search")
}

func (a *API) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	events, total, err := a.store.ListAuditEvents(store.AuditFilter{
		Actor: q.Get("actor"), Action: q.Get("action"), Resource: q.Get("resource"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

func (a *API) handleGetAudit(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /audit/{id}")
}

func (a *API) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	events, _, _ := a.store.ListAuditEvents(store.AuditFilter{Limit: 10000})
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.ndjson"`)
	enc := json.NewEncoder(w)
	for _, e := range events {
		_ = enc.Encode(e)
	}
}

// ============================================================================
// Overview / Host / Logs / Traffic
// ============================================================================

func (a *API) handleOverview(w http.ResponseWriter, r *http.Request) {
	vms := a.store.ListVMs()
	running := 0
	for _, vm := range vms {
		if vm.State == types.StateRunning {
			running++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": a.version, "running": running, "total_vms": len(vms),
		"projects": len(a.store.ListProjects()), "images": len(a.store.ListGoldenImages()),
		"hostname": hostname(), "host": hostname(), "uptime": uptimeSecs(),
		"baseDomain": a.baseDomain,
	})
}

func hostname() string {
	h, err := osHostname()
	if err != nil {
		return "porter-host"
	}
	return h
}

func uptimeSecs() int {
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if secs, ferr := strconv.ParseFloat(fields[0], 64); ferr == nil {
				return int(secs)
			}
		}
	}
	return 0
}

func hostLoad() (ncpu int, load float64, memUsed, memTotal int64) {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		ncpu = strings.Count(string(b), "processor\t:")
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f, ferr := strconv.ParseFloat(strings.Fields(string(b))[0], 64); ferr == nil {
			load = f
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			fs := strings.Fields(line)
			if len(fs) < 2 {
				continue
			}
			v, _ := strconv.ParseInt(fs[1], 10, 64)
			switch fs[0] {
			case "MemTotal:":
				memTotal = v
			case "MemAvailable:":
				memUsed = memTotal - v
			}
		}
	}
	return
}

func (a *API) handleHostOverview(w http.ResponseWriter, r *http.Request) {
	cpu, load, mu, mt := hostLoad()
	cpuPct := 0.0
	if load001 := load; load001 > 0 && cpu > 0 {
		cpuPct = load001 / float64(cpu) * 100
	}
	memPct := 0.0
	if mt > 0 {
		memPct = float64(mu) / float64(mt) * 100
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hostname": hostname(), "cpu": cpuPct, "mem": memPct,
		"uptime": uptimeSecs(), "version": a.version,
		"cpu_cores": cpu, "mem_total_mb": mt / 1024,
	})
}

func (a *API) handleDaemonLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"logs": a.store.TailDaemonLogs(tailN(r))})
}

func (a *API) handleHostPorts(w http.ResponseWriter, r *http.Request) {
	ports := make([]map[string]any, 0)
	for _, vm := range a.store.ListVMs() {
		for _, p := range vm.Ports {
			ports = append(ports, map[string]any{
				"vm_id": vm.ID, "name": vm.Name, "ip": vm.IPAddress,
				"host": p.HostPort, "container": p.ContainerPort, "proto": p.Protocol,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ports": ports, "host": hostname()})
}

func (a *API) handleHostKernel(w http.ResponseWriter, r *http.Request) {
	for _, p := range []string{
		"/etc/porter/kernel/vmlinux", "/opt/porter/kernel/vmlinux",
		"/var/lib/porter/kernel/vmlinux", "kernel/vmlinux",
	} {
		if fi, err := os.Stat(p); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"kernel": "firecracker", "path": p, "size": fi.Size(), "modified": fi.ModTime(),
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "vmlinux not found on host; run `porter kernel set <url>` (see scripts/backend/install.sh)")
}

func (a *API) handleHostPrerequisites(w http.ResponseWriter, r *http.Request) {
	if a.hostConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false, "configured": false, "checks": []startup.Result{}})
		return
	}
	checks := startup.Check(a.hostConfig)
	ready := true
	for _, check := range checks {
		if !check.OK {
			ready = false
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": ready, "configured": true, "checks": checks})
}

func (a *API) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if a.hostConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	cfg := a.hostConfig
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "runtime_mode": cfg.RuntimeMode,
		"firecracker_bin": cfg.FirecrackerBin, "jailer_bin": cfg.JailerBin,
		"api_socket_dir": cfg.FirecrackerSocketDir, "kernel_image": cfg.KernelImage,
		"rootfs_path": cfg.RootfsPath, "images_dir": cfg.ImagesDir,
		"custom_images_dir": cfg.CustomImagesDir, "logs_dir": cfg.LogsDir,
		"gateway_enabled": cfg.GatewayEnabled, "health_enabled": cfg.HealthEnabled, "ssh_enabled": cfg.SSHEnabled,
	})
}

func (a *API) handleAllTraffic(w http.ResponseWriter, r *http.Request) {
	out := []*types.TrafficEntry{}
	for _, vm := range a.store.ListVMs() {
		out = append(out, a.store.ListTraffic(vm.ID, 100)...)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleClearTraffic(w http.ResponseWriter, r *http.Request) {
	a.store.ClearTraffic()
	writeJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
}

func (a *API) handleTrafficSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	results := []*types.TrafficEntry{}
	for _, vm := range a.store.ListVMs() {
		for _, e := range a.store.ListTraffic(vm.ID, 100) {
			if q == "" || strings.Contains(strings.ToLower(e.Path), q) || strings.Contains(strings.ToLower(e.Host), q) || strings.Contains(e.Method, q) {
				results = append(results, e)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "query": q})
}

func (a *API) handleServerAnalytics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.GetServer(id); !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	from, to, step := serverDetailWindow(r)
	series := a.store.ServerMetricsTimeseries(id, from, to, step)
	for _, m := range []string{"cpu_percent", "memory_mib"} {
		if series[m] == nil {
			series[m] = []store.ServerDetailPoint{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id": id, "from": from, "to": to, "step_seconds": step, "series": series,
		"source":  "per-VM metrics_samples (cpu_percent, memory_mib) aggregated over placements",
		"planned": map[string]any{"disk": nil, "network": nil, "host_cpu": nil, "host_memory": nil},
	})
}

func (a *API) handleTeamAuditLog(w http.ResponseWriter, r *http.Request) {
	events, total, _ := a.store.ListAuditEvents(store.AuditFilter{Limit: 200})
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

// ============================================================================
// Embedded dashboard + Prometheus gauge snapshot
// ============================================================================

func PGGaugeSnapshot(st *store.Store, volMgr *volumes.Manager, version string) observability.Snapshot {
	var snap observability.Snapshot
	if st == nil {
		return snap
	}
	byVM := map[string]*observability.VMGauge{}
	for _, vm := range st.ListVMs() {
		if vm == nil {
			continue
		}
		snap.Total++
		if vm.State == types.StateRunning {
			snap.Running++
		}
		byVM[vm.ID] = &observability.VMGauge{ID: vm.ID, State: string(vm.State)}
	}
	for _, m := range st.LatestMetrics() {
		g, ok := byVM[m.VMID]
		if !ok {
			continue
		}
		switch m.Metric {
		case "cpu_percent":
			g.CPU = m.Value * 10
		case "memory_mib":
			g.MemMiB = m.Value
		case "disk_bytes":
			g.DiskB = m.Value
		case "net_rx_bytes":
			g.RxB = m.Value
		case "net_tx_bytes":
			g.TxB = m.Value
		case "gpu_percent":
			g.GPU = m.Value
		}
	}
	for _, g := range byVM {
		snap.VMs = append(snap.VMs, *g)
		snap.SysCPU += g.CPU
		snap.SysMem += g.MemMiB
		snap.SysDisk += g.DiskB
		snap.SysRx += g.RxB
		snap.SysTx += g.TxB
	}
	snap.Version = version
	snap.SysDisk = 0
	for i := range snap.VMs {
		if d, ok := measuredDisk(st, volMgr, snap.VMs[i].ID); ok {
			snap.VMs[i].DiskB = float64(d)
		}
		snap.SysDisk += snap.VMs[i].DiskB
	}
	return snap
}

func measuredDisk(st *store.Store, volMgr *volumes.Manager, vmID string) (int64, bool) {
	vm, ok := st.GetVM(vmID)
	if !ok || vm == nil {
		return 0, false
	}
	var total int64
	if vm.RootfsPath != "" {
		if fi, err := os.Stat(vm.RootfsPath); err == nil {
			total += fi.Size()
		}
	}
	if volMgr != nil && vm.VolumeID != "" {
		if n, err := volMgr.Usage(vm.VolumeID); err == nil {
			total += n
		}
	}
	return total, true
}
