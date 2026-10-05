// Servers, nodes/enrollment, providers/regions/zones/node-pools, capacity, K8s.
package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"porter/internal/agent"
	"porter/internal/deployment"
	"porter/internal/policy"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Servers / nodes / providers / regions / zones / node pools
// ============================================================================

func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListServers())
}

func (a *API) handleRegisterServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
		Address  string `json:"address"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	srv := &types.Server{ID: store.NewID(), Name: req.Hostname, Address: req.Address, Status: "registered", CreatedAt: time.Now()}
	a.store.PutServer(srv)
	a.store.AppendDaemonLog(fmt.Sprintf("server registered: %s (%s)", req.Hostname, req.Address))
	writeJSON(w, http.StatusCreated, srv)
}

func (a *API) handleImportServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
		Address  string `json:"address"`
		Token    string `json:"token"`
	}
	if err := decodeBody(r, &req); err != nil || req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	if req.Hostname == "" {
		req.Hostname = req.Address
	}
	srv := &types.Server{ID: store.NewID(), Name: req.Hostname, Address: req.Address, Status: "imported", CreatedAt: time.Now()}
	a.store.PutServer(srv)
	writeJSON(w, http.StatusCreated, srv)
}

func (a *API) handleGetServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.store.GetServer(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (a *API) handleServerHeartbeat(w http.ResponseWriter, r *http.Request) {
	var h types.ServerHeartbeat
	if err := readJSON(r, &h); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if h.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = h.ID
	}
	tok := r.Header.Get("X-Porter-Node-Token")
	if tok == "" {
		if ah := r.Header.Get("Authorization"); len(ah) > 7 && ah[:7] == "Bearer " {
			tok = ah[7:]
		}
	}
	if !a.store.ServerNodeTokenOK(id, tok) {
		writeError(w, http.StatusUnauthorized, "invalid or missing node token — enroll via POST /nodes/enroll")
		return
	}
	if h.Status == "" {
		h.Status = "online"
	}
	if !a.store.ApplyHeartbeat(&h) {
		writeError(w, http.StatusNotFound, "server not registered — POST /servers first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": h.ID})
}

func (a *API) handleServerSSH(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.store.GetServer(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	host, port := srv.Address, 22
	if h, p, err := net.SplitHostPort(srv.Address); err == nil {
		host = h
		if n, perr := strconv.Atoi(p); perr == nil && n > 0 {
			port = n
		}
	}
	info := &types.ServerSSH{Host: host, Port: port, User: "root", Gateway: "direct"}
	if host == "" {
		info.Host, info.Gateway, info.Port, info.User = "localhost", "porter", 2222, "admin"
	}
	writeJSON(w, http.StatusOK, info)
}

func (a *API) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteServer(r.PathValue("id")) {
		a.store.AppendDaemonLog("server unregistered: " + r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "server not found")
}

func (a *API) handleServerDetail(w http.ResponseWriter, r *http.Request) {
	d, ok := a.store.GetServerDetail(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server": d.Server, "heartbeat": d.Heartbeat, "capacity": d.Capacity,
		"vm_counts": d.VMCounts, "vms_placed": d.VMsPlaced, "recent_logs": d.RecentLogs,
		"planned": map[string]any{"region": nil, "zone": nil, "host_metrics": nil},
	})
}

func (a *API) handleServerVMs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.GetServer(id); !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	vms := a.store.ServerVMsList(id, serverDetailLimit(r, 200, 1000))
	if vms == nil {
		vms = []store.ServerVMRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": id, "vms": vms, "count": len(vms)})
}

func (a *API) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.GetServer(id); !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	logs := a.store.ServerDaemonLogs(id, serverDetailLimit(r, 100, 1000))
	if logs == nil {
		logs = []store.ServerLogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": id, "logs": logs, "count": len(logs)})
}

func serverDetailWindow(r *http.Request) (from, to time.Time, step int) {
	now := time.Now()
	to = now
	from = now.Add(-time.Hour)
	step = 60
	q := r.URL.Query()
	if v := q.Get("minutes"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			if mins > 7*24*60 {
				mins = 7 * 24 * 60
			}
			from = now.Add(-time.Duration(mins) * time.Minute)
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	if v := q.Get("step"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			step = n
			if step > 3600 {
				step = 3600
			}
		}
	}
	return
}

func serverDetailLimit(r *http.Request, def, maxCap int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > maxCap {
				return maxCap
			}
			return n
		}
	}
	return def
}

func (a *API) handleServerCordon(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /servers/{id}/cordon")
}

func (a *API) handleServerUncordon(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /servers/{id}/uncordon")
}

func (a *API) handleServerDrain(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("node-drain", "server", r.PathValue("id"), "drain|"+r.PathValue("id"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

// canonicalDisruptionBudget validates and normalises the disruption budget a
// node drain or maintenance operation is allowed to violate. Exactly one bound
// may be supplied — min_available (keep at least N replicas serving) or
// max_unavailable (take at most N down) — and it must be a non-negative whole
// number. Unknown keys are rejected rather than ignored, so a typo can never
// silently degrade into "no budget at all", which would let a drain take every
// replica down at once.
func canonicalDisruptionBudget(body map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, raw := range body {
		switch key {
		case "min_available", "max_unavailable":
		default:
			return nil, fmt.Errorf("unknown disruption budget field %q", key)
		}
		var n int
		switch v := raw.(type) {
		case int:
			n = v
		case int64:
			n = int(v)
		case float64:
			if v != float64(int(v)) {
				return nil, fmt.Errorf("%s must be a whole number", key)
			}
			n = int(v)
		default:
			return nil, fmt.Errorf("%s must be a number", key)
		}
		if n < 0 {
			return nil, fmt.Errorf("%s must not be negative", key)
		}
		out[key] = n
	}
	if _, hasMin := out["min_available"]; hasMin {
		if _, hasMax := out["max_unavailable"]; hasMax {
			return nil, fmt.Errorf("set only one of min_available or max_unavailable")
		}
	}
	return out, nil
}

func (a *API) handleServerUpgrade(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("node-upgrade", "server", r.PathValue("id"), "upgrade|"+r.PathValue("id"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleServerRecover(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("node-recover", "server", r.PathValue("id"), "recover|"+r.PathValue("id"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleServerReplace(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("node-replace", "server", r.PathValue("id"), "replace|"+r.PathValue("id"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tokens": a.store.ListEnrollmentTokens()})
}

func (a *API) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var req enrollmentTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid enrollment body")
		return
	}
	if req.Provider == "" {
		req.Provider = "customer_owned"
	}
	ttl := time.Duration(req.TTLHours) * time.Hour
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = 24 * time.Hour
	}
	tok, err := agent.NewEnrollmentToken(req.Provider, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mint enrollment token")
		return
	}
	labels, _ := json.Marshal(req.Labels)
	if string(labels) == "" || string(labels) == "null" {
		labels = []byte("{}")
	}
	if err := a.store.CreateEnrollmentToken(tok.Token, tok.Provider, string(labels), tok.ExpiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "store enrollment token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": tok.Token, "provider": tok.Provider,
		"expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (a *API) handleRevokeEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteEnrollmentToken(r.PathValue("id")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
		return
	}
	writeError(w, http.StatusNotFound, "token not found")
}

type enrollmentTokenReq struct {
	Provider string            `json:"provider"`
	TTLHours int               `json:"ttl_hours"`
	Labels   map[string]string `json:"labels"`
}

type nodeEnrollReq struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	NodeID   string `json:"node_id"`
}

func (a *API) handleNodeEnroll(w http.ResponseWriter, r *http.Request) {
	var req nodeEnrollReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid enrollment body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname is required")
		return
	}
	if err := agent.ValidateToken(req.Token); err != nil {
		writeError(w, http.StatusBadRequest, "malformed enrollment token")
		return
	}
	row, ok := a.store.GetEnrollmentToken(req.Token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid enrollment token")
		return
	}
	if row.UsedBy != "" {
		writeError(w, http.StatusConflict, "enrollment token already used by node "+row.UsedBy)
		return
	}
	if !time.Now().Before(row.ExpiresAt) {
		writeError(w, http.StatusGone, "enrollment token expired; mint a new one")
		return
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = req.Hostname
	}
	if !a.store.ConsumeEnrollmentToken(req.Token, nodeID) {
		writeError(w, http.StatusConflict, "enrollment token already used or expired")
		return
	}
	srv := &types.Server{ID: store.NewID(), Name: req.Hostname, Address: req.Address, Status: "registered", CreatedAt: time.Now()}
	a.store.PutServer(srv)
	nodeToken := "pnrt_" + hex.EncodeToString(randomBytes(24))
	if err := a.store.SetServerNodeToken(srv.ID, sha256Hex(nodeToken)); err != nil {
		writeError(w, http.StatusInternalServerError, "node token persistence failed: "+err.Error())
		return
	}
	a.store.AppendDaemonLog(fmt.Sprintf("node %s enrolled via enrollment token (provider %s)", nodeID, row.Provider))
	writeJSON(w, http.StatusCreated, map[string]any{
		"server": srv, "node_id": nodeID, "provider": row.Provider,
		"node_token": nodeToken, "heartbeat_path": "/servers/" + srv.ID + "/heartbeat",
	})
}

// Providers / regions / zones / node pools

func (a *API) handleListProviders(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /providers")
}

func (a *API) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /providers")
}

func (a *API) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /providers/{id}")
}

func (a *API) handlePatchProvider(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /providers/{id}")
}

func (a *API) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /providers/{id}")
}

func (a *API) handleListRegions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /regions")
}

func (a *API) handleCreateRegion(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /regions")
}

func (a *API) handleGetRegion(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /regions/{id}")
}

func (a *API) handlePatchRegion(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /regions/{id}")
}

func (a *API) handleDeleteRegion(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /regions/{id}")
}

func (a *API) handleListZones(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /zones")
}

func (a *API) handleCreateZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /zones")
}

func (a *API) handleGetZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /zones/{id}")
}

func (a *API) handlePatchZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /zones/{id}")
}

func (a *API) handleDeleteZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /zones/{id}")
}

func (a *API) handleListNodePools(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /node-pools")
}

func (a *API) handleCreateNodePool(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /node-pools")
}

func (a *API) handleGetNodePool(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /node-pools/{id}")
}

func (a *API) handlePatchNodePool(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /node-pools/{id}")
}

func (a *API) handleDeleteNodePool(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /node-pools/{id}")
}

func (a *API) handleListNodePoolNodes(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /node-pools/{id}/nodes")
}

func (a *API) handleAddNodePoolNode(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /node-pools/{id}/nodes/{nodeId}")
}

func (a *API) handleRemoveNodePoolNode(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /node-pools/{id}/nodes/{nodeId}")
}

// Capacity + placements + scheduler decisions

func (a *API) handleListCapacityPolicies(w http.ResponseWriter, r *http.Request) {
	rows := a.store.ListCapacityPolicies()
	var pols []policy.CapacityPolicy
	for _, row := range rows {
		var scope policy.Scope
		switch row.Scope {
		case "host":
			scope = policy.ScopeHost
		case "pool":
			scope = policy.ScopePool
		case "provider":
			scope = policy.ScopeProvider
		default:
			scope = policy.ScopeGlobal
		}
		pols = append(pols, policy.CapacityPolicy{
			Scope: scope, CPUOvercommit: row.CPUOvercommit,
			MemOvercommit: row.MemOvercommit, ReserveVCPU: row.ReserveVCPU,
			ReserveMemMiB: row.ReserveMemMiB, Enabled: row.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": rows, "effective": policy.Merge(pols...)})
}

type capacityPolicyReq struct {
	Scope         string   `json:"scope"`
	ScopeID       string   `json:"scope_id"`
	CPUOvercommit *float64 `json:"cpu_overcommit"`
	MemOvercommit *float64 `json:"mem_overcommit"`
	ReserveVCPU   *int     `json:"reserve_vcpu"`
	ReserveMemMiB *int     `json:"reserve_mem_mib"`
	Enabled       *bool    `json:"enabled"`
}

func (a *API) handlePutCapacityPolicy(w http.ResponseWriter, r *http.Request) {
	var req capacityPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid capacity policy body")
		return
	}
	switch req.Scope {
	case "host", "pool", "provider", "global":
	default:
		writeError(w, http.StatusBadRequest, "scope must be host|pool|provider|global")
		return
	}
	if err := a.store.UpsertCapacityPolicy(store.CapacityPolicyRow{
		Scope: req.Scope, ScopeID: req.ScopeID,
		CPUOvercommit: req.CPUOvercommit, MemOvercommit: req.MemOvercommit,
		ReserveVCPU: req.ReserveVCPU, ReserveMemMiB: req.ReserveMemMiB, Enabled: req.Enabled,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "upsert capacity policy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) handleGetCapacity(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /capacity")
}

func (a *API) handleListPlacements(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /scheduler/placements")
}

func (a *API) handleListSchedulingDecisions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /scheduler/decisions")
}

// ============================================================================
// K8s (extension — host/adopt, not a K8s API server)
// ============================================================================

func (a *API) handleListK8sClusters(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters")
}

func (a *API) handleCreateK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/clusters")
}

func (a *API) handleGetK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}")
}

func (a *API) handleDeleteK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /k8s/clusters/{id}")
}

func (a *API) handleScaleK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/clusters/{id}/scale")
}

func (a *API) handleUpgradeK8sCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := decodeBody(r, &req); err != nil || req.Version == "" {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("k8s-upgrade", "cluster", r.PathValue("id"), "upgrade|"+r.PathValue("id")+"|"+req.Version, map[string]string{"version": req.Version})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleK8sKubeconfig(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/kubeconfig")
}

func (a *API) handleListK8sNodes(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/nodes")
}

func (a *API) handleK8sCordonNode(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/clusters/{id}/nodes/{nodeId}/cordon")
}

func (a *API) handleK8sUncordonNode(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/clusters/{id}/nodes/{nodeId}/uncordon")
}

func (a *API) handleK8sDrainNode(w http.ResponseWriter, r *http.Request) {
	opID, err := a.store.CreateOperationWithPayload("k8s-drain", "cluster", r.PathValue("id"), "drain|"+r.PathValue("nodeId"), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleListK8sWorkloads(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/workloads")
}

func (a *API) handleListK8sEvents(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/events")
}

func (a *API) handleK8sPodLogs(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/logs/{namespace}/{pod}")
}

func (a *API) handleK8sPodExec(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "K8s pod exec (requires in-cluster agent + kubelet exec API)")
}

func (a *API) handleAdoptK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/adopt")
}

func (a *API) handleReleaseK8sCluster(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/clusters/{id}/release")
}

func (a *API) handleK8sAgentEnroll(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /k8s/agents/enroll")
}

func (a *API) handleListK8sAgents(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/agents")
}

func (a *API) handleGetK8sAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/agents/{id}")
}

func (a *API) handleDeleteK8sAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /k8s/agents/{id}")
}

func (a *API) handleK8sAgentStatus(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /k8s/clusters/{id}/agent-status")
}

// ============================================================================
// Operator admin surface (server sub-pages, credentials, destinations,
// storages, notifications, shared variables, instance settings, profile,
// terminal, uploads/downloads, sources, databases, misc)
// ============================================================================

func (a *API) handleServerAdvanced(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /servers/{id}/advanced")
}

func (a *API) handleServerProxy(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "proxy")
}

func (a *API) handleServerProxyDynamicConfs(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "proxy/dynamic")
}

func (a *API) handleServerProxyLogs(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.store.GetServer(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": srv.ID, "logs": a.store.ServerDaemonLogs(srv.ID, 200)})
}

func (a *API) handleServerCloudflareTunnel(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "cloudflare-tunnel")
}

func (a *API) handleServerLogDrains(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /servers/{id}/log-drains")
}

func (a *API) handleUpdateServerLogDrains(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /servers/{id}/log-drains")
}

func (a *API) handleServerCloudProviderToken(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "cloud-provider-token")
}

func (a *API) handleValidateServer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /servers/{id}/validate")
}

func (a *API) handleListServerEnv(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /servers/{id}/envs")
}

func (a *API) handleCreateServerEnv(w http.ResponseWriter, r *http.Request) {
	a.createSharedEnv(w, r, "server", r.PathValue("id"))
}

func (a *API) handlePatchServerEnv(w http.ResponseWriter, r *http.Request) {
	a.patchSharedEnv(w, r, "server", r.PathValue("id"), r.PathValue("envId"))
}

func (a *API) handleDeleteServerEnv(w http.ResponseWriter, r *http.Request) {
	a.deleteSharedEnv(w, r, "server", r.PathValue("id"), r.PathValue("envId"))
}

func (a *API) handleGetSettingsOAuthProvider(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "oauth/"+r.PathValue("provider"))
}

func (a *API) handleUpdateSettingsOAuthProvider(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsPut(w, r, "oauth/"+r.PathValue("provider"))
}

// recovered from internal/api/handlers.go
func (a *API) handlePoolDrain(w http.ResponseWriter, r *http.Request) {
	// Real drain: persist the draining state, stop every live replica, and
	// report how many were actually stopped.
	projID := a.projectID(r)
	a.store.PutProjectSettings(projID, "pool", map[string]any{"draining": true, "drained_at": time.Now().Format(time.RFC3339)})
	n, failures := a.mutateReplicas(projID, nil, "stop")
	a.store.AppendDaemonLog(fmt.Sprintf("pool drained for project %s (%d replicas stopped)", projID, n))
	status := http.StatusAccepted
	if len(failures) > 0 && n == 0 {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"status": "draining", "project_id": projID, "stopped": n, "failed": failures})
}

// recovered from internal/api/handlers.go
func (a *API) handlePoolStatus(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	pool := map[string]any{"desired": proj.ReplicasDesired, "healthy": 0, "draining": 0, "vms": proj.VMIDs}
	for _, vid := range proj.VMIDs {
		if vm, ok := a.store.GetVM(vid); ok && vm.HealthStatus == types.HealthHealthy {
			pool["healthy"] = pool["healthy"].(int) + 1
		}
	}
	writeJSON(w, http.StatusOK, pool)
}

// recovered from internal/api/feature_more.go
func (a *API) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	var req configPushReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid config-push body")
		return
	}
	projectID := r.PathValue("projectId")
	push := deployment.PushRequest{ServiceID: projectID, BaseRev: req.BaseRev, Env: req.Env}
	if err := deployment.ValidatePush(push, req.BaseRev); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	envRaw, _ := json.Marshal(req.Env)
	opID, err := a.store.CreateOperationWithPayload("config-push", "project", projectID,
		"config-push|"+projectID+"|"+req.BaseRev,
		map[string]string{"base_rev": req.BaseRev, "env_json": string(envRaw)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record config-push")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"op_id": opID, "state": "QUEUED", "applied": false,
		"note": "intent recorded; task runner applies by compare-and-swap",
	})
}

// from feature_more.go
type configPushReq struct {
	BaseRev string            `json:"base_rev"`
	Env     map[string]string `json:"env"`
}
