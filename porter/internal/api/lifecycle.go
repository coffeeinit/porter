package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *Server) listAppEnv(w http.ResponseWriter, r *http.Request) {
	env, err := s.Store.ListAppEnv(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for i := range env {
		if env[i].Secret {
			env[i].Value = "••••••••"
		}
	}
	writeJSON(w, 200, env)
}

func (s *Server) putAppEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string `json:"key"`
		Value  string `json:"value"`
		Secret bool   `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.PutAppEnv(r.Context(), r.PathValue("id"), req.Key, req.Value, req.Secret); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "saved"})
}

func (s *Server) deleteAppEnv(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteAppEnv(r.Context(), r.PathValue("id"), r.PathValue("key")); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := s.Store.ListDeployments(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, deps)
}

func (s *Server) rollbackApp(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.RollbackApp(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, d)
}

var _ = strconv.Itoa
