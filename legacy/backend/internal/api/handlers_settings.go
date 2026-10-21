// Project and instance settings surfaces.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"porter/internal/email"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Settings (per-section generic handlers)
// ============================================================================

func (a *API) handleGetGeneral(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "general")
}

func (a *API) handlePatchGeneral(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "general")
}

func (a *API) handleGetBuild(w http.ResponseWriter, r *http.Request) { a.settingsGet(w, r, "build") }

func (a *API) handlePutBuild(w http.ResponseWriter, r *http.Request) { a.settingsPut(w, r, "build") }

func (a *API) handleGetChecks(w http.ResponseWriter, r *http.Request) { a.settingsGet(w, r, "checks") }

func (a *API) handlePutChecks(w http.ResponseWriter, r *http.Request) { a.settingsPut(w, r, "checks") }

func (a *API) handleGetRollout(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "rollout")
}

func (a *API) handlePutRollout(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "rollout")
}

func (a *API) handleGetBuildMachine(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "build-machine")
}

func (a *API) handlePutBuildMachine(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "build-machine")
}

func (a *API) handleGetFramework(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		a.settingsGet(w, r, "framework")
		return
	}
	if saved := a.store.GetProjectSettings(proj.ID, "framework"); len(saved) > 0 {
		writeJSON(w, http.StatusOK, saved)
		return
	}
	writeJSON(w, http.StatusOK, detectFramework(proj))
}

func (a *API) handleGetGit(w http.ResponseWriter, r *http.Request) { a.settingsGet(w, r, "git") }

func (a *API) handlePutGit(w http.ResponseWriter, r *http.Request) { a.settingsPut(w, r, "git") }

func (a *API) handleGetGitLFS(w http.ResponseWriter, r *http.Request) { a.settingsGet(w, r, "git/lfs") }

func (a *API) handlePutGitLFS(w http.ResponseWriter, r *http.Request) { a.settingsPut(w, r, "git/lfs") }

func (a *API) handleGetProtection(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "deployment-protection")
}

func (a *API) handlePutProtection(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "deployment-protection")
}

func (a *API) handleGetSecurity(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "security")
}

func (a *API) handlePutSecurity(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "security")
}

func (a *API) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "retention")
}

func (a *API) handlePutRetention(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "retention")
}

func (a *API) handleGetNetworking(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "networking")
}

func (a *API) handlePutNetworking(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "networking")
}

func (a *API) handleGetAdvanced(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "advanced")
}

func (a *API) handlePutAdvanced(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "advanced")
}

func (a *API) handleGetOIDC(w http.ResponseWriter, r *http.Request) { a.settingsGet(w, r, "oidc") }

func (a *API) handlePutOIDC(w http.ResponseWriter, r *http.Request) { a.settingsPut(w, r, "oidc") }

func (a *API) handleGetPassport(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "passport")
}

func (a *API) handlePutPassport(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "passport")
}

func (a *API) handleGetMicrofrontends(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "microfrontends")
}

func (a *API) handlePutMicrofrontends(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "microfrontends")
}

func (a *API) handleGetFunctions(w http.ResponseWriter, r *http.Request) {
	a.settingsGet(w, r, "functions")
}

func (a *API) handlePutFunctions(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "functions")
}

func (a *API) handleSetIgnoreCommand(w http.ResponseWriter, r *http.Request) {
	a.settingsPut(w, r, "ignore-command")
}

func (a *API) settingsGet(w http.ResponseWriter, r *http.Request, section string) {
	data := a.store.GetProjectSettings(a.projectID(r), section)
	if data == nil {
		data = map[string]any{}
	}
	writeJSON(w, http.StatusOK, data)
}

func (a *API) settingsPut(w http.ResponseWriter, r *http.Request, section string) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	a.store.PutProjectSettings(a.projectID(r), section, body)
	writeJSON(w, http.StatusOK, body)
}

func (a *API) handleListDrains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListDrains(a.projectID(r)))
}

func (a *API) handleCreateDrain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
		Kind     string `json:"kind"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	d := &types.Drain{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, Endpoint: req.Endpoint, Kind: req.Kind, Active: true, CreatedAt: time.Now()}
	a.store.PutDrain(d)
	writeJSON(w, http.StatusCreated, d)
}

func (a *API) handleDeleteDrain(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteDrain(r.PathValue("drainId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "drain not found")
}

func (a *API) handleTestDrain(w http.ResponseWriter, r *http.Request) {
	drains := a.store.ListDrains(a.projectID(r))
	var drain *types.Drain
	for _, d := range drains {
		if d.ID == r.PathValue("drainId") {
			drain = d
			break
		}
	}
	if drain == nil {
		writeError(w, http.StatusNotFound, "drain not found")
		return
	}
	if drain.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "drain has no endpoint")
		return
	}
	body := fmt.Sprintf(`{"drain":%q,"test":true,"ts":%q}`, drain.ID, time.Now().Format(time.RFC3339))
	if _, err := http.Post(drain.Endpoint, "application/json", strings.NewReader(body)); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"drain": drain.ID, "status": "delivery failed", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drain": drain.ID, "status": "delivered test event"})
}

func (a *API) emailService() (*email.Service, error) {
	if a.emailSvc != nil {
		return a.emailSvc, nil
	}
	if a.mailer == nil {
		return nil, fmt.Errorf("SMTP is not configured (mailer is nil)")
	}
	a.emailSvc = email.NewService(a.mailer, 60)
	return a.emailSvc, nil
}

func (a *API) syncEmailIdentities(projectID string, svc *email.Service) error {
	raw, err := a.projectSecret(projectID, "email/identities")
	if err != nil {
		return nil
	}
	var ids []email.DomainIdentity
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return err
	}
	for _, id := range ids {
		if err := svc.Register(id); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) handleEmailIdentity(w http.ResponseWriter, r *http.Request) {
	var id email.DomainIdentity
	if err := readJSON(r, &id); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	id.Enabled = true
	if err := id.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := a.projectID(r)
	var ids []email.DomainIdentity
	if raw, err := a.projectSecret(projectID, "email/identities"); err == nil {
		_ = json.Unmarshal([]byte(raw), &ids)
	}
	kept := ids[:0]
	for _, cur := range ids {
		if !strings.EqualFold(cur.Domain, id.Domain) {
			kept = append(kept, cur)
		}
	}
	ids = append(kept, id)
	raw, _ := json.Marshal(ids)
	if err := a.putProjectSecret(projectID, "email/identities", string(raw)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"domain": id.Domain, "from": id.From("")})
}

func (a *API) handleEmailSend(w http.ResponseWriter, r *http.Request) {
	var msg email.Message
	if err := readJSON(r, &msg); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	svc, err := a.emailService()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := a.syncEmailIdentities(a.projectID(r), svc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := svc.Send(msg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

func (a *API) handleGetSettingsEmail(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "email")
}

func (a *API) handleUpdateSettingsEmail(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsPut(w, r, "email")
}

func (a *API) handleGetSettingsOAuth(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "oauth")
}

func (a *API) handleUpdateSettingsOAuth(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsPut(w, r, "oauth")
}

func (a *API) handleGetSettingsAdvanced(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "advanced")
}

func (a *API) handleUpdateSettingsAdvanced(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsPut(w, r, "advanced")
}

func (a *API) handleGetSettingsUpdates(w http.ResponseWriter, r *http.Request) {
	a.instanceSettingsGet(w, r, "updates")
}
