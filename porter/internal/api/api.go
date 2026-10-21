package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"porter/controlplane/internal/store"
	"porter/controlplane/web"
	"porter/protocol"
)

type Server struct {
	Store      *store.Store
	AdminToken string
	BaseDomain string
	buildMu    sync.Mutex
	building   map[string]context.CancelFunc
}

func New(st *store.Store, adminToken, baseDomain string) *Server {
	return &Server{Store: st, AdminToken: adminToken, BaseDomain: baseDomain, building: map[string]context.CancelFunc{}}
}

type ctxKey string

const nodeIDKey ctxKey = "node_id"

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": protocol.Version})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(web.Index)
	})
	mux.HandleFunc("GET /v1/nodes", s.admin(s.listNodes))
	mux.HandleFunc("GET /v1/fleet/nodes/{id}", s.admin(s.getNode))
	mux.HandleFunc("GET /v1/fleet/nodes/{id}/metrics", s.admin(s.nodeMetrics))
	mux.HandleFunc("GET /v1/fleet/nodes/{id}/logs", s.admin(s.nodeLogs))
	mux.HandleFunc("GET /v1/fleet/nodes/{id}/vms", s.admin(s.nodeVMs))
	mux.HandleFunc("POST /v1/fleet/nodes/{id}/cordon", s.admin(s.cordonNode))
	mux.HandleFunc("POST /v1/fleet/nodes/{id}/uncordon", s.admin(s.uncordonNode))
	mux.HandleFunc("POST /v1/fleet/nodes/{id}/drain", s.admin(s.drainNode))
	mux.HandleFunc("POST /v1/fleet/join-tokens", s.admin(s.createJoinToken))
	mux.HandleFunc("POST /v1/fleet/enroll", s.enroll)
	mux.HandleFunc("POST /v1/fleet/heartbeat", s.node(s.heartbeat))
	mux.HandleFunc("GET /v1/fleet/commands", s.node(s.commands))
	mux.HandleFunc("POST /v1/fleet/result", s.node(s.result))
	mux.HandleFunc("GET /v1/projects", s.admin(s.listProjects))
	mux.HandleFunc("GET /v1/apps", s.admin(s.listAllApps))
	mux.HandleFunc("POST /v1/projects", s.admin(s.createProject))
	mux.HandleFunc("GET /v1/projects/{id}/apps", s.admin(s.listApps))
	mux.HandleFunc("POST /v1/projects/{id}/apps", s.admin(s.createApp))
	mux.HandleFunc("POST /v1/apps/{id}/deployments", s.admin(s.createDeployment))
	mux.HandleFunc("POST /v1/apps/{id}/scale", s.admin(s.scaleApp))
	mux.HandleFunc("GET /v1/apps/{id}/autoscale", s.admin(s.getAutoscale))
	mux.HandleFunc("PUT /v1/apps/{id}/autoscale", s.admin(s.putAutoscale))
	mux.HandleFunc("POST /v1/apps/{id}/activity", s.admin(s.appActivity))
	mux.HandleFunc("POST /v1/apps/{id}/wake", s.admin(s.wakeApp))
	mux.HandleFunc("POST /v1/apps/{id}/previews", s.admin(s.createPreview))
	mux.HandleFunc("GET /v1/apps/{id}/env", s.admin(s.listAppEnv))
	mux.HandleFunc("POST /v1/apps/{id}/env", s.admin(s.putAppEnv))
	mux.HandleFunc("DELETE /v1/apps/{id}/env/{key}", s.admin(s.deleteAppEnv))
	mux.HandleFunc("GET /v1/apps/{id}/deployments", s.admin(s.listDeployments))
	mux.HandleFunc("POST /v1/apps/{id}/rollback", s.admin(s.rollbackApp))
	mux.HandleFunc("GET /v1/apps/{id}/replicas", s.admin(s.appReplicas))
	mux.HandleFunc("GET /v1/deployments/{id}", s.admin(s.getDeployment))
	mux.HandleFunc("POST /v1/system/backup", s.admin(s.createBackup))
	mux.HandleFunc("POST /v1/system/backup/verify", s.admin(s.verifyBackup))
	mux.HandleFunc("GET /v1/operations", s.admin(s.listOperations))
	mux.HandleFunc("GET /v1/events", s.admin(s.listEvents))
	mux.HandleFunc("POST /mcp", s.admin(s.handleMCP))
	mux.HandleFunc("GET /mcp", s.admin(s.mcpInfo))
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/projects/{id}/volumes", s.admin(s.listVolumes))
	mux.HandleFunc("POST /v1/projects/{id}/volumes", s.admin(s.createVolume))
	mux.HandleFunc("POST /v1/volumes/{id}/attach", s.admin(s.attachVolume))
	mux.HandleFunc("GET /v1/snapshots", s.admin(s.listSnapshots))
	mux.HandleFunc("POST /v1/snapshots", s.admin(s.createSnapshot))
	mux.HandleFunc("GET /v1/logs", s.admin(s.listLogs))
	mux.HandleFunc("GET /v1/metrics", s.admin(s.listMetrics))
	mux.HandleFunc("GET /v1/images", s.admin(s.listImages))
	mux.HandleFunc("POST /v1/images", s.admin(s.createImage))
	mux.HandleFunc("GET /v1/images/{id}", s.admin(s.getImage))
	mux.HandleFunc("GET /v1/images/{id}/logs", s.admin(s.imageLogs))
	mux.HandleFunc("POST /v1/images/build", s.admin(s.buildImage))
	mux.HandleFunc("POST /v1/images/{id}/cancel", s.admin(s.cancelImageBuild))
	mux.HandleFunc("POST /v1/images/detect", s.admin(s.detectFramework))
	mux.HandleFunc("POST /v1/volumes/{id}/resize", s.admin(s.resizeVolume))
	mux.HandleFunc("POST /v1/replicas/{id}/weight", s.admin(s.setReplicaWeight))
	mux.HandleFunc("POST /v1/snapshots/{id}/restore", s.admin(s.restoreSnapshot))
	mux.HandleFunc("GET /v1/routes", s.admin(s.listRoutes))
	mux.HandleFunc("POST /v1/routes", s.admin(s.createRoute))
	mux.HandleFunc("GET /v1/domains", s.admin(s.listDomains))
	mux.HandleFunc("GET /v1/projects/{id}/domains", s.admin(s.listProjectDomains))
	mux.HandleFunc("POST /v1/projects/{id}/domains", s.admin(s.createDomain))
	mux.HandleFunc("POST /v1/domains/{id}/verify", s.admin(s.verifyDomain))
	mux.HandleFunc("GET /v1/gateway/stats", s.admin(s.gatewayStats))
	mux.HandleFunc("GET /v1/gateway/requests", s.admin(s.gatewayRequests))
	mux.HandleFunc("GET /v1/traces", s.admin(s.listTraces))
	mux.HandleFunc("POST /v1/fleet/logs", s.node(s.ingestLogs))
	mux.HandleFunc("POST /v1/fleet/snapshot", s.node(s.ingestSnapshot))
	mux.HandleFunc("POST /v1/fleet/vmstate", s.node(s.ingestVMState))
	mux.HandleFunc("POST /hooks/github", s.githubWebhook)
	return mux
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.AdminToken == "" || tok != s.AdminToken {
			writeError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) node(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.Header.Get("X-Porter-Node-ID")
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if nodeID == "" || !s.Store.AuthNode(r.Context(), nodeID, tok) {
			writeError(w, http.StatusUnauthorized, "valid node token required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), nodeIDKey, nodeID)))
	}
}

func (s *Server) createJoinToken(w http.ResponseWriter, r *http.Request) {
	ttl := 15 * time.Minute
	var req struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	tok, err := s.Store.CreateJoinToken(r.Context(), ttl)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]interface{}{"token": tok, "expires_in_seconds": int(ttl.Seconds())})
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	resp, err := s.Store.Enroll(r.Context(), req)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}
	writeJSON(w, 201, resp)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var hb protocol.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if hb.NodeID != r.Context().Value(nodeIDKey).(string) {
		writeError(w, 403, "node id mismatch")
		return
	}
	if err := s.Store.Heartbeat(r.Context(), hb); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	nodeID := r.Context().Value(nodeIDKey).(string)
	cmds, err := s.Store.ClaimCommands(r.Context(), nodeID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cmds)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	var res protocol.CommandResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if res.NodeID != r.Context().Value(nodeIDKey).(string) {
		writeError(w, 403, "node id mismatch")
		return
	}
	if err := s.Store.CompleteCommand(r.Context(), res); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.Store.ListNodes(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, nodes)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ListProjects(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	p, err := s.Store.CreateProject(r.Context(), req.Name)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListApps(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, apps)
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		Image           string `json:"image"`
		Port            int    `json:"port"`
		VCPUs           int    `json:"vcpus"`
		MemoryMB        int    `json:"memory_mb"`
		Replicas        int    `json:"replicas"`
		PersistenceMode string `json:"persistence_mode"`
		VolumeSizeMB    int    `json:"volume_size_mb"`
		Repo            string `json:"repo"`
		Branch          string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	a, err := s.Store.CreateApp(r.Context(), r.PathValue("id"), req.Name, req.Image, req.Port, req.VCPUs, req.MemoryMB, req.Replicas)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Repo != "" {
		_ = s.Store.SetAppSource(r.Context(), a.ID, req.Repo, req.Branch)
	}
	if req.PersistenceMode != "" {
		_ = s.Store.SetAppPersistence(r.Context(), a.ID, req.PersistenceMode, req.VolumeSizeMB)
		a.PersistenceMode = req.PersistenceMode
		if req.VolumeSizeMB > 0 {
			a.VolumeSizeMB = req.VolumeSizeMB
		}
	}
	if s.BaseDomain != "" {
		host := slug(a.Name) + "." + s.BaseDomain
		_, _ = s.Store.CreateDomain(r.Context(), a.ProjectID, a.ID, host, "production")
	}
	writeJSON(w, 201, a)
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image    string `json:"image"`
		Replicas int    `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	d, err := s.Store.CreateDeployment(r.Context(), r.PathValue("id"), req.Image, req.Replicas)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, d)
}

func (s *Server) scaleApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replicas int `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.ScaleApp(r.Context(), r.PathValue("id"), req.Replicas); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]interface{}{"status": "accepted", "replicas": req.Replicas})
}

func (s *Server) listOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := s.Store.ListOperations(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ops)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	evts, err := s.Store.ListEvents(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, evts)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
