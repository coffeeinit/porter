package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
)

const maxVolumeSizeMiB int64 = 16 * 1024 * 1024

func (s *Server) handleProjectInstances(w http.ResponseWriter, r *http.Request, projectID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if _, err := s.repository.GetProject(r.Context(), projectID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "project-instances", projectID)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "project-instances", projectID)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListProjectInstancesPage(r.Context(), projectID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Instance]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("project-instances", projectID, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleProjectVolumes(w http.ResponseWriter, r *http.Request, projectID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if _, err := s.repository.GetProject(r.Context(), projectID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	s.listVolumes(w, r, projectID, "", "project-volumes", projectID)
}

func (s *Server) handleProjectServiceVolumes(w http.ResponseWriter, r *http.Request, projectID, serviceID string) {
	switch r.Method {
	case http.MethodGet:
		if err := s.requireServiceInProject(w, r, projectID, serviceID); err != nil {
			return
		}
		s.listVolumes(w, r, projectID, serviceID, "service-volumes", projectID+"/"+serviceID)
	case http.MethodPost:
		if err := s.requireServiceInProject(w, r, projectID, serviceID); err != nil {
			return
		}
		s.createVolume(w, r, projectID, serviceID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) requireServiceInProject(w http.ResponseWriter, r *http.Request, projectID, serviceID string) error {
	service, err := s.repository.GetService(r.Context(), serviceID)
	if err != nil {
		writeRepositoryError(w, err)
		return err
	}
	if service.ProjectID != projectID {
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "resource not found")
		return pgstore.ErrNotFound
	}
	return nil
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request, projectID, serviceID, resource, scope string) {
	pageSize, cursorText, ok := s.timePageParams(w, r, resource, scope)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, resource, scope)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListVolumesPage(r.Context(), projectID, serviceID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Volume]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor(resource, scope, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request, projectID, serviceID string) {
	var request v1.CreateVolumeRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.MountPath = strings.TrimSpace(request.MountPath)
	request.NodeAffinityID = strings.TrimSpace(request.NodeAffinityID)
	if !validName(request.Name) || request.SizeMiB < 1 || request.SizeMiB > maxVolumeSizeMiB {
		badPageQuery(w, "volume name and a size from 1 MiB to 16 TiB are required")
		return
	}
	if request.AccessMode != "" && request.AccessMode != v1.VolumeSingleWriter {
		badPageQuery(w, "only single_writer volume declarations are supported")
		return
	}
	if !validMountPath(request.MountPath) {
		badPageQuery(w, "mount_path must be a safe absolute path")
		return
	}
	volume, err := s.repository.CreateVolume(r.Context(), projectID, serviceID, request)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/volumes/"+url.PathEscape(volume.ID))
	writeJSON(w, http.StatusCreated, volume)
}

func validMountPath(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 4096 || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#\\%\x00\r\n\t ") {
		return false
	}
	if value == "/" {
		return true
	}
	if strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '.' && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

func (s *Server) handleVolume(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	volume, err := s.repository.GetVolume(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, volume)
}

func (s *Server) handleProjectSnapshots(w http.ResponseWriter, r *http.Request, projectID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if _, err := s.repository.GetProject(r.Context(), projectID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	s.listSnapshots(w, r, projectID, "", "project-snapshots", projectID)
}

func (s *Server) handleInstanceSnapshots(w http.ResponseWriter, r *http.Request, instanceID string) {
	switch r.Method {
	case http.MethodGet:
		instance, err := s.repository.GetInstance(r.Context(), instanceID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		service, err := s.repository.GetService(r.Context(), instance.ServiceID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		s.listSnapshots(w, r, service.ProjectID, instanceID, "instance-snapshots", instanceID)
	case http.MethodPost:
		var request v1.CreateSnapshotMetadataRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			bodyError(w)
			return
		}
		if request.Consistency != v1.SnapshotCrashConsistent {
			badPageQuery(w, "only crash_consistent snapshot metadata is supported")
			return
		}
		snapshot, err := s.repository.CreateSnapshotMetadata(r.Context(), instanceID, request.Consistency)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		w.Header().Set("Location", v1.BasePath+"/snapshots/"+url.PathEscape(snapshot.ID))
		writeJSON(w, http.StatusCreated, snapshot)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleSnapshotCapture(w http.ResponseWriter, r *http.Request, instanceID string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "snapshot capture does not accept query parameters")
		return
	}
	var request v1.SnapshotOperationRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	if request.Consistency != v1.SnapshotCrashConsistent || !validName(request.IdempotencyKey) || len(request.IdempotencyKey) > 128 {
		badPageQuery(w, "crash_consistent capture and an idempotency_key are required")
		return
	}
	snapshot, operation, err := s.repository.QueueSnapshotCreateOperation(r.Context(), instanceID, request.Consistency, request.IdempotencyKey)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/operations/"+url.PathEscape(operation.ID))
	writeJSON(w, http.StatusAccepted, v1.SnapshotOperationResponse{Snapshot: snapshot, Operation: operation})
}

func (s *Server) handleSnapshotRestore(w http.ResponseWriter, r *http.Request, instanceID, snapshotID string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "snapshot restore does not accept query parameters")
		return
	}
	var request v1.SnapshotOperationRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	if request.Consistency != "" || !validName(request.IdempotencyKey) || len(request.IdempotencyKey) > 128 {
		badPageQuery(w, "restore requires an idempotency_key; consistency is taken from the verified snapshot")
		return
	}
	snapshot, operation, err := s.repository.QueueSnapshotRestoreOperation(r.Context(), instanceID, snapshotID, request.IdempotencyKey)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/operations/"+url.PathEscape(operation.ID))
	writeJSON(w, http.StatusAccepted, v1.SnapshotOperationResponse{Snapshot: snapshot, Operation: operation})
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request, projectID, instanceID, resource, scope string) {
	pageSize, cursorText, ok := s.timePageParams(w, r, resource, scope)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, resource, scope)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListSnapshotMetadataPage(r.Context(), projectID, instanceID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.SnapshotMetadata]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor(resource, scope, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSnapshotMetadata(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	snapshot, err := s.repository.GetSnapshotMetadata(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleProjectBackups(w http.ResponseWriter, r *http.Request, projectID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if _, err := s.repository.GetProject(r.Context(), projectID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	s.listBackups(w, r, projectID, "", "project-backups", projectID)
}

func (s *Server) handleSnapshotBackups(w http.ResponseWriter, r *http.Request, snapshotID string) {
	snapshot, err := s.repository.GetSnapshotMetadata(r.Context(), snapshotID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listBackups(w, r, snapshot.ProjectID, snapshotID, "snapshot-backups", snapshotID)
	case http.MethodPost:
		var request v1.CreateBackupMetadataRequest
		if err := decodeJSONBody(w, r, &request); err != nil {
			bodyError(w)
			return
		}
		if request.Consistency != v1.SnapshotCrashConsistent {
			badPageQuery(w, "only crash_consistent backup metadata is supported")
			return
		}
		backup, err := s.repository.CreateBackupMetadata(r.Context(), snapshotID, request.Consistency)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		w.Header().Set("Location", v1.BasePath+"/backups/"+url.PathEscape(backup.ID))
		writeJSON(w, http.StatusCreated, backup)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request, projectID, snapshotID, resource, scope string) {
	pageSize, cursorText, ok := s.timePageParams(w, r, resource, scope)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, resource, scope)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListBackupMetadataPage(r.Context(), projectID, snapshotID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.BackupMetadata]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor(resource, scope, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleBackupMetadata(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	backup, err := s.repository.GetBackupMetadata(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, backup)
}
