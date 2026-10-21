// Databases and database backups.
package api

import (
	"net/http"
)

// Databases

func (a *API) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases")
}

func (a *API) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /databases")
}

func (a *API) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}")
}

func (a *API) handlePatchDatabase(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /databases/{id}")
}

func (a *API) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /databases/{id}")
}

func (a *API) handleListDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}/backups")
}

func (a *API) handleCreateDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /databases/{id}/backups")
}

func (a *API) handleGetDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}/backups/{backupUuid}")
}

func (a *API) handleListDatabaseBackupExecutions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}/backups/{backupUuid}/executions")
}

func (a *API) handleDatabaseBackupS3(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}/backups/{backupUuid}/s3")
}

func (a *API) handleDatabaseBackupRetention(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /databases/{id}/backups/{backupUuid}/retention")
}

func (a *API) handleDatabaseBackupDanger(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"backup": r.PathValue("backupUuid"), "actions": []string{"delete", "restore"}})
}

func (a *API) handleDatabaseImportBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := decodeBody(r, &req); err != nil || req.ExecutionID == "" {
		writeError(w, http.StatusBadRequest, "execution_id is required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("db-import-backup", "database", r.PathValue("id"), "import|"+req.ExecutionID, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}
