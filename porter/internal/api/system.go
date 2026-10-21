package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"porter/controlplane/internal/store"
)

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, ok := s.Store.GetDeployment(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "deployment not found")
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) appReplicas(w http.ResponseWriter, r *http.Request) {
	reps, err := s.Store.ListAppReplicas(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(reps))
	for _, x := range reps {
		out = append(out, map[string]interface{}{"id": x.ID, "deployment_id": x.DeploymentID, "state": x.State, "health": x.HealthStatus, "traffic_weight": x.TrafficWeight})
	}
	writeJSON(w, 200, out)
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	dir := os.Getenv("PORTER_BACKUP_DIR")
	if dir == "" {
		dir = "/var/lib/porter/backups"
	}
	path := filepath.Join(dir, "porter-"+time.Now().UTC().Format("20060102-150405")+".db")
	if err := s.Store.BackupTo(path); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	sum, err := store.FileSHA256(path)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"backup_path": path, "sha256": sum})
}

func (s *Server) verifyBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := store.VerifyBackup(req.Path); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	sum, _ := store.FileSHA256(req.Path)
	writeJSON(w, 200, map[string]string{"status": "ok", "sha256": sum})
}
