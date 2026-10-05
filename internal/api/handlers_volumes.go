// Volumes, storage classes, object stores, buckets, backups, snapshots.
package api

import (
	"net/http"
	"sort"
	"time"

	"porter/internal/cron"
	"porter/internal/store"
	"porter/internal/types"
	"porter/internal/volumes"
)

func (a *API) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, paginate(w, r, a.store.ListVolumes(a.projectID(r))))
}

func (a *API) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		SizeMiB   int    `json:"size_mib"`
		MountPath string `json:"mount_path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.SizeMiB <= 0 {
		req.SizeMiB = 1024
	}
	v := &types.Volume{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, SizeMiB: req.SizeMiB, Path: req.MountPath, CreatedAt: time.Now()}
	if a.volMgr != nil {
		hostPath, err := a.volMgr.Create(volumes.SanitizeID(v.ID), v.SizeMiB)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to provision volume: "+err.Error())
			return
		}
		v.Path = hostPath
	}
	a.store.PutVolume(v)
	writeJSON(w, http.StatusCreated, v)
}

func (a *API) handleGetVolume(w http.ResponseWriter, r *http.Request) {
	if v, ok := a.store.GetVolume(r.PathValue("volumeId")); ok {
		if projectID := r.PathValue("projectId"); projectID != "" && v.ProjectID != projectID {
			writeError(w, http.StatusNotFound, "volume not found")
			return
		}
		writeJSON(w, http.StatusOK, v)
		return
	}
	writeError(w, http.StatusNotFound, "volume not found")
}

func (a *API) handleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("volumeId")
	if v, ok := a.store.GetVolume(id); !ok || (r.PathValue("projectId") != "" && v.ProjectID != r.PathValue("projectId")) {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if a.store.DeleteVolume(id) {
		if a.volMgr != nil {
			_ = a.volMgr.Delete(volumes.SanitizeID(id))
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "volume not found")
}

func (a *API) handleResizeVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SizeMiB int `json:"size_mib"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if v, ok := a.store.GetVolume(r.PathValue("volumeId")); !ok || (r.PathValue("projectId") != "" && v.ProjectID != r.PathValue("projectId")) {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if a.store.ResizeVolume(r.PathValue("volumeId"), req.SizeMiB) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "resized", "size_mib": req.SizeMiB})
		return
	}
	writeError(w, http.StatusNotFound, "volume not found")
}

func (a *API) handleVolumeUsage(w http.ResponseWriter, r *http.Request) {
	v, ok := a.store.GetVolume(r.PathValue("volumeId"))
	if !ok {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if projectID := r.PathValue("projectId"); projectID != "" && v.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	var used int64
	if a.volMgr != nil {
		used, _ = a.volMgr.Usage(volumes.SanitizeID(v.ID))
	}
	limit := int64(v.SizeMiB) * 1024 * 1024
	path := v.Path
	if path == "" && a.volMgr != nil {
		path = a.volMgr.Path(volumes.SanitizeID(v.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"used_bytes": used, "limit_bytes": limit, "volume": v.ID, "path": path})
}

func (a *API) handleCloneVolume(w http.ResponseWriter, r *http.Request) {
	srcID := r.PathValue("volumeId")
	src, ok := a.store.GetVolume(srcID)
	if !ok {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if projectID := r.PathValue("projectId"); projectID != "" && src.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	if a.volMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "volume manager unavailable")
		return
	}
	dstID := store.NewID()
	hostPath, err := a.volMgr.Clone(volumes.SanitizeID(srcID), volumes.SanitizeID(dstID), src.SizeMiB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clone volume: "+err.Error())
		return
	}
	v := &types.Volume{ID: dstID, ProjectID: a.projectID(r), Name: src.Name + "-clone", SizeMiB: src.SizeMiB, Path: hostPath, CreatedAt: time.Now()}
	a.store.PutVolume(v)
	writeJSON(w, http.StatusCreated, v)
}

// Storage classes (SRS §31)

func (a *API) handleListStorageClasses(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /storage-classes")
}

func (a *API) handleCreateStorageClass(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /storage-classes")
}

func (a *API) handleGetStorageClass(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /storage-classes/{id}")
}

func (a *API) handlePatchStorageClass(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /storage-classes/{id}")
}

func (a *API) handleDeleteStorageClass(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /storage-classes/{id}")
}

// Object stores + buckets (SRS §32)

func (a *API) handleListObjectStores(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /object-stores")
}

func (a *API) handleCreateObjectStore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /object-stores")
}

func (a *API) handleGetObjectStore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /object-stores/{id}")
}

func (a *API) handlePatchObjectStore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /object-stores/{id}")
}

func (a *API) handleDeleteObjectStore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /object-stores/{id}")
}

func (a *API) handleValidateObjectStore(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /object-stores/{id}/validate")
}

func (a *API) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /object-stores/{id}/buckets")
}

func (a *API) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /object-stores/{id}/buckets")
}

func (a *API) handleDeleteBucket(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /object-stores/{id}/buckets/{bucket}")
}

// Backups + policies + snapshots (SRS §33)

func (a *API) handleListBackups(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	backups := make([]store.BackupRow, 0)
	for _, vm := range a.store.ListVMs() {
		if vm == nil || vm.ProjectID != projectID {
			continue
		}
		backups = append(backups, a.store.ListBackups(vm.ID)...)
	}
	sort.SliceStable(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	if len(backups) > 100 {
		backups = backups[:100]
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups, "project_id": projectID})
}

func (a *API) handleListBackupSchedules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"schedules": a.store.ListBackupSchedules(a.projectID(r), false)})
}

func (a *API) handlePutBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Workload  string `json:"workload"`
		Cron      string `json:"cron"`
		Retention int    `json:"retention"`
	}
	if err := readJSON(r, &req); err != nil || req.Cron == "" {
		writeError(w, http.StatusBadRequest, "cron is required")
		return
	}
	if !cron.Validate(req.Cron) {
		writeError(w, http.StatusBadRequest, "bad cron expression (want 5 fields)")
		return
	}
	id, err := a.store.PutBackupSchedule(a.projectID(r), req.Workload, req.Cron, req.Retention)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (a *API) handleListAllBackups(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /backups")
}

func (a *API) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /backups/{id}")
}

func (a *API) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /backups/{id}")
}

func (a *API) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	_ = decodeBody(r, &req)
	if req.Target == "" {
		req.Target = "original"
	}
	opID, err := a.store.CreateOperationWithPayload("restore-backup", "backup", r.PathValue("id"), "restore|"+r.PathValue("id")+"|"+req.Target,
		map[string]string{"target": req.Target})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED", "target": req.Target})
}

func (a *API) handleVerifyRestoreBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /backups/{id}/verify-restore")
}

func (a *API) handleListBackupPolicies(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /backups/policies")
}

func (a *API) handleCreateBackupPolicy(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /backups/policies")
}

func (a *API) handleGetBackupPolicy(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /backups/policies/{id}")
}

func (a *API) handlePatchBackupPolicy(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /backups/policies/{id}")
}

func (a *API) handleDeleteBackupPolicy(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /backups/policies/{id}")
}

func (a *API) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /snapshots, GET /vms/{replicaId}/snapshots")
}

func (a *API) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /snapshots/{id}")
}

func (a *API) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /snapshots/{id}")
}

func (a *API) handleSnapshotHistory(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /snapshots/{id}/history")
}

func (a *API) handleBranchSnapshot(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /snapshots/{id}/branch")
}

func (a *API) handleSnapshotDiff(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /snapshots/{id}/diff")
}

func (a *API) handleGetSettingsBackup(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "backup")
}

func (a *API) handleUpdateSettingsBackup(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsPut(w, r, "backup")
}

func (a *API) handleDownloadVolumeBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /download/volume-backup/{executionId}")
}

// recovered from internal/api/handlers.go
func (a *API) handleReplicaSnapshot(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /projects/{projectId}/replicas/{n}/snapshot")
}

// recovered from internal/api/handlers.go
func (a *API) handleReplicaSnapshotByID(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /vms/{replicaId}/snapshot")
}
