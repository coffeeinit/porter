package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.ListDomains(r.Context(), "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) listProjectDomains(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.ListDomains(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID    string `json:"app_id"`
		Hostname string `json:"hostname"`
		Kind     string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Hostname == "" {
		writeError(w, 400, "hostname required")
		return
	}
	d, err := s.Store.CreateDomain(r.Context(), r.PathValue("id"), req.AppID, req.Hostname, req.Kind)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, d)
}

func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.VerifyDomain(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func (s *Server) gatewayStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Store.GatewayStats(r.Context()))
}

func (s *Server) gatewayRequests(w http.ResponseWriter, r *http.Request) {
	logs, err := s.Store.ListRequestLogs(r.Context(), 100)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PRNumber int `json:"pr_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.PRNumber <= 0 {
		writeError(w, 400, "pr_number required")
		return
	}
	if s.BaseDomain == "" {
		writeError(w, 400, "base domain not configured")
		return
	}
	d, err := s.Store.CreatePreviewDomain(r.Context(), r.PathValue("id"), req.PRNumber, s.BaseDomain)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, d)
}

func (s *Server) listTraces(w http.ResponseWriter, r *http.Request) {
	logs, err := s.Store.ListRequestLogs(r.Context(), 200)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}
