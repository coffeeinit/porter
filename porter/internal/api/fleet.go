package api

import (
	"net/http"
	"strconv"

	"porter/controlplane/internal/store"
)

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	n, ok := s.Store.GetNode(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "node not found")
		return
	}
	writeJSON(w, 200, n)
}

func (s *Server) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	m, err := s.Store.ListNodeMetrics(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) nodeLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	l, err := s.Store.ListNodeLogs(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, l)
}

func (s *Server) nodeVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := s.Store.ListNodeVMs(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, publicNodeVMs(vms))
}

func publicNodeVMs(vms []store.Replica) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(vms))
	for _, v := range vms {
		out = append(out, map[string]interface{}{"id": v.ID, "replica_id": v.ID, "deployment_id": v.DeploymentID, "app_id": v.AppID, "state": v.State, "health": v.HealthStatus})
	}
	return out
}

func (s *Server) cordonNode(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetNodeState(r.Context(), r.PathValue("id"), "cordoned"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cordoned"})
}

func (s *Server) uncordonNode(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetNodeState(r.Context(), r.PathValue("id"), "ready"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

func (s *Server) drainNode(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetNodeState(r.Context(), r.PathValue("id"), "draining"); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "draining"})
}
