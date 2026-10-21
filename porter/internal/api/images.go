package api

import (
	"encoding/json"
	"net/http"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := s.Store.ListImages(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, imgs)
}

func (s *Server) createImage(w http.ResponseWriter, r *http.Request) {
	var img store.ImageRecord
	if err := json.NewDecoder(r.Body).Decode(&img); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if img.Name == "" || img.SourceType == "" {
		writeError(w, 400, "name and source_type required")
		return
	}
	created, err := s.Store.CreateImage(r.Context(), img)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, created)
}

func (s *Server) resizeVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SizeMB int `json:"size_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.ResizeVolume(r.Context(), r.PathValue("id"), req.SizeMB); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	opID, err := s.Store.QueueVolumeResize(r.Context(), r.PathValue("id"), req.SizeMB)
	if err != nil {
		writeJSON(w, 202, map[string]string{"status": "metadata_resized", "warning": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]string{"status": "resize_queued", "operation_id": opID})
}

func (s *Server) setReplicaWeight(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Weight int `json:"weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.SetReplicaWeight(r.Context(), r.PathValue("id"), req.Weight); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "ok"})
}

func (s *Server) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.Store.GetSnapshot(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "snapshot not found")
		return
	}
	spec := map[string]string{"vm_id": snap.VMID, "state_path": snap.StatePath, "mem_path": snap.MemPath}
	opID, err := s.Store.QueueVMCommand(r.Context(), snap.VMID, "snapshot.restore", "snapshot.restore", spec)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": opID, "status": "restore queued"})
}

var _ = protocol.Version
