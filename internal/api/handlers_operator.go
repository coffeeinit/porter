// Operator admin surface: the person running Porter administers their own
// installation here — deploy destinations, storage backends, S3/backup
// targets, notification channels, instance settings, credentials
// (SSH keys, cloud and integration tokens, cloud-init), API tokens,
// terminal sessions and onboarding.
package api

import (
	"net/http"
)

func (a *API) handleServerDockerCleanup(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "docker-cleanup")
}

func (a *API) handleRunServerDockerCleanup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.GetServer(id); !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("docker-cleanup", "server", id, "cleanup|"+id, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleServerSentinel(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "sentinel")
}

func (a *API) handleServerSentinelLogs(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.store.GetServer(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": srv.ID, "logs": a.store.ServerDaemonLogs(srv.ID, 200)})
}

func (a *API) handleServerSecurityPatches(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "security/patches")
}

func (a *API) handleServerTerminalAccess(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "security/terminal-access")
}

func (a *API) handleServerPrivateKey(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "private-key")
}

func (a *API) handleServerDestinations(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /servers/{id}/destinations")
}

func (a *API) settingsGetForServer(w http.ResponseWriter, r *http.Request, section string) {
	notImplemented(w, "GET /servers/{id}/"+section)
}

// Security — private keys / cloud tokens / integration tokens / cloud-init / api tokens

func (a *API) handleListPrivateKeys(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/private-keys")
}

func (a *API) handleCreatePrivateKey(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/private-keys")
}

func (a *API) handleGetPrivateKey(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/private-keys/{id}")
}

func (a *API) handleDeletePrivateKey(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /security/private-keys/{id}")
}

func (a *API) handleListCloudTokens(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/cloud-tokens")
}

func (a *API) handleCreateCloudToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/cloud-tokens")
}

func (a *API) handleGetCloudToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/cloud-tokens/{id}")
}

func (a *API) handlePatchCloudToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /security/cloud-tokens/{id}")
}

func (a *API) handleDeleteCloudToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /security/cloud-tokens/{id}")
}

func (a *API) handleValidateCloudToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/cloud-tokens/{id}/validate")
}

func (a *API) handleListIntegrationTokens(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/integration-tokens")
}

func (a *API) handleCreateIntegrationToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/integration-tokens")
}

func (a *API) handleDeleteIntegrationToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /security/integration-tokens/{id}")
}

func (a *API) handleListCloudInitScripts(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/cloud-init-scripts")
}

func (a *API) handleCreateCloudInitScript(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/cloud-init-scripts")
}

func (a *API) handleGetCloudInitScript(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/cloud-init-scripts/{id}")
}

func (a *API) handlePatchCloudInitScript(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /security/cloud-init-scripts/{id}")
}

func (a *API) handleDeleteCloudInitScript(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /security/cloud-init-scripts/{id}")
}

func (a *API) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /security/api-tokens")
}

func (a *API) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /security/api-tokens")
}

func (a *API) handleDeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /security/api-tokens/{id}")
}

// Destinations / storages / s3-storages

func (a *API) handleListDestinations(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /destinations")
}

func (a *API) handleCreateDestination(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /destinations")
}

func (a *API) handleGetDestination(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /destinations/{id}")
}

func (a *API) handlePatchDestination(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /destinations/{id}")
}

func (a *API) handleDeleteDestination(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /destinations/{id}")
}

func (a *API) handleDestinationResources(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /destinations/{id}/resources")
}

func (a *API) handleListStorages(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /storages")
}

func (a *API) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /storages")
}

func (a *API) handleGetStorage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /storages/{id}")
}

func (a *API) handlePatchStorage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /storages/{id}")
}

func (a *API) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /storages/{id}")
}

func (a *API) handleListS3Storages(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /s3-storages")
}

func (a *API) handleCreateS3Storage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /s3-storages")
}

func (a *API) handleGetS3Storage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /s3-storages/{id}")
}

func (a *API) handlePatchS3Storage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /s3-storages/{id}")
}

func (a *API) handleDeleteS3Storage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /s3-storages/{id}")
}

func (a *API) handleValidateS3Storage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /s3-storages/{id}/validate")
}

// Notifications

func (a *API) notificationGet(w http.ResponseWriter, r *http.Request, channel string) {
	notImplemented(w, "GET /notifications/"+channel)
}

func (a *API) notificationPut(w http.ResponseWriter, r *http.Request, channel string) {
	notImplemented(w, "PATCH /notifications/"+channel)
}

func (a *API) handleGetNotificationEmail(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "email")
}

func (a *API) handleUpdateNotificationEmail(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "email")
}

func (a *API) handleGetNotificationTelegram(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "telegram")
}

func (a *API) handleUpdateNotificationTelegram(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "telegram")
}

func (a *API) handleGetNotificationDiscord(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "discord")
}

func (a *API) handleUpdateNotificationDiscord(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "discord")
}

func (a *API) handleGetNotificationSlack(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "slack")
}

func (a *API) handleUpdateNotificationSlack(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "slack")
}

func (a *API) handleGetNotificationPushover(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "pushover")
}

func (a *API) handleUpdateNotificationPushover(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "pushover")
}

func (a *API) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	a.notifyProject(a.projectID(r), "Porter test notification", "This is a test from the notifications endpoint.")
	writeJSON(w, http.StatusOK, map[string]any{"status": "test sent"})
}

// Instance settings

func (a *API) handleGetInstanceSettings(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /settings")
}

func (a *API) handlePatchInstanceSettings(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /settings")
}

func (a *API) instanceSettingsGet(w http.ResponseWriter, r *http.Request, section string) {
	notImplemented(w, "GET /settings/"+section)
}

func (a *API) instanceSettingsPut(w http.ResponseWriter, r *http.Request, section string) {
	notImplemented(w, "PATCH /settings/"+section)
}

// Terminal

func (a *API) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": []string{"/terminal/auth", "/terminal/session"}})
}

func (a *API) handleTerminalAuth(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "user": currentUser(r)})
}

func (a *API) handleTerminalAuthIPs(w http.ResponseWriter, r *http.Request) {
	ips := []string{}
	for _, s := range a.store.ListServers() {
		if s != nil && s.Address != "" {
			ips = append(ips, s.Address)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ipAddresses": ips})
}

func (a *API) handleTerminalSession(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /terminal/session")
}

// Uploads / downloads

func (a *API) handleUploadBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /upload/backup/{databaseUuid}")
}

func (a *API) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /download/backup/{executionId}")
}

func (a *API) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /onboarding")
}

func (a *API) handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"admin": true, "org_id": a.orgIDFromHeader(r)})
}

// recovered from internal/api/handlers.go
func (a *API) handleSSHToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	proj.SSHEnabled = req.Enabled
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"ssh_enabled": req.Enabled})
}
