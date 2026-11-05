package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"

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
	if !restorableSnapshot(snap) {
		writeError(w, http.StatusConflict, "snapshot is not restorable: crash-consistent state, memory, root-disk, or attached-volume integrity metadata is incomplete")
		return
	}
	spec := protocol.SnapshotRestoreSpec{SnapshotID: snap.ID, VMID: snap.VMID, StatePath: snap.StatePath, MemPath: snap.MemPath, SizeBytes: snap.SizeBytes, StateSHA256: snap.StateSHA256, MemSHA256: snap.MemSHA256, DiskFiles: snap.DiskFiles}
	opID, err := s.Store.QueueVMCommand(r.Context(), snap.VMID, "snapshot.restore", "snapshot.restore", spec)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": opID, "status": "restore queued"})
}

func restorableSnapshot(snap store.SnapshotRecord) bool {
	if snap.ID == "" || snap.VMID == "" || snap.SizeBytes <= 0 || snap.StatePath == "" || snap.MemPath == "" || !filepath.IsAbs(snap.StatePath) || !filepath.IsAbs(snap.MemPath) || filepath.Clean(snap.StatePath) == filepath.Clean(snap.MemPath) || !validSnapshotHex(snap.StateSHA256) || !validSnapshotHex(snap.MemSHA256) || len(snap.DiskFiles) == 0 || len(snap.DiskFiles) > 2 {
		return false
	}
	seen := make(map[string]bool, len(snap.DiskFiles))
	paths := map[string]bool{filepath.Clean(snap.StatePath): true, filepath.Clean(snap.MemPath): true}
	var diskBytes int64
	for _, file := range snap.DiskFiles {
		if (file.Kind != "rootfs" && file.Kind != "volume") || seen[file.Kind] || file.SizeBytes <= 0 || !validSnapshotHex(file.SHA256) || !filepath.IsAbs(file.OriginalPath) || !filepath.IsAbs(file.BackupPath) {
			return false
		}
		original, backup := filepath.Clean(file.OriginalPath), filepath.Clean(file.BackupPath)
		if original == backup || paths[original] || paths[backup] {
			return false
		}
		paths[original], paths[backup], seen[file.Kind] = true, true, true
		diskBytes += file.SizeBytes
	}
	return seen["rootfs"] && snap.SizeBytes > diskBytes
}

func validSnapshotHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

var _ = protocol.Version
