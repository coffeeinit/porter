package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"porter/controlplane/internal/store"
)

func (s *Server) getAutoscale(w http.ResponseWriter, r *http.Request) {
	a, ok := s.Store.GetApp(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "app not found")
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"app_id": a.ID, "enabled": a.AutoscaleEnabled, "min_replicas": a.MinReplicas,
		"max_replicas": a.MaxReplicas, "cpu_target": a.CPUTarget,
		"idle_timeout_sec": a.IdleTimeoutSec, "scale_to_zero": a.ScaleToZero,
		"replicas_desired": a.ReplicasDesired, "last_cpu_percent": a.LastCPUPercent,
		"last_activity_at": a.LastActivityAt,
	})
}

func (s *Server) putAutoscale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled        bool `json:"enabled"`
		MinReplicas    int  `json:"min_replicas"`
		MaxReplicas    int  `json:"max_replicas"`
		CPUTarget      int  `json:"cpu_target"`
		IdleTimeoutSec int  `json:"idle_timeout_sec"`
		ScaleToZero    bool `json:"scale_to_zero"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.MinReplicas < 0 || req.MaxReplicas < 1 || req.MaxReplicas < req.MinReplicas {
		writeError(w, 400, "invalid replica bounds")
		return
	}
	if req.CPUTarget < 1 || req.CPUTarget > 100 {
		writeError(w, 400, "cpu_target must be between 1 and 100")
		return
	}
	if req.IdleTimeoutSec < 0 {
		writeError(w, 400, "idle_timeout_sec must be >= 0")
		return
	}
	if err := s.Store.PutAutoscalePolicy(r.Context(), r.PathValue("id"), req.Enabled, req.MinReplicas, req.MaxReplicas, req.CPUTarget, req.IdleTimeoutSec, req.ScaleToZero); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "saved"})
}

func (s *Server) appActivity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CPUPercent float64 `json:"cpu_percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.TouchApp(r.Context(), r.PathValue("id"), req.CPUPercent); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func wakeTarget(a store.App) (int, error) {
	if a.MinReplicas < 0 || a.MaxReplicas < 1 || a.MaxReplicas < a.MinReplicas {
		return 0, errors.New("invalid stored replica bounds")
	}
	target := a.MinReplicas
	if target < 1 {
		target = 1
	}
	if target > a.MaxReplicas {
		target = a.MaxReplicas
	}
	return target, nil
}

func (s *Server) wakeApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.Store.GetApp(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "app not found")
		return
	}
	target, err := wakeTarget(a)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.Store.ScaleApp(r.Context(), a.ID, target); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.Store.TouchApp(r.Context(), a.ID, 0); err != nil {
		writeError(w, 500, fmt.Sprintf("app woke but activity update failed: %v", err))
		return
	}
	writeJSON(w, 202, map[string]interface{}{"status": "waking", "replicas": target})
}

func (s *Server) listAllApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListAllApps(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, apps)
}

func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.Store.ListRoutes(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, routes)
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host       string `json:"host"`
		AppID      string `json:"app_id"`
		PathPrefix string `json:"path_prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Host == "" || req.AppID == "" {
		writeError(w, 400, "host and app_id required")
		return
	}
	rt, err := s.Store.CreateRoute(r.Context(), req.Host, req.AppID, req.PathPrefix)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, rt)
}
