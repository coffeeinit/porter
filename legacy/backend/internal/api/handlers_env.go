// Environment variables, secrets, shared env.
package api

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"porter/internal/secretbox"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Env & Secrets
// ============================================================================

func (a *API) handleListEnv(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	out := make([]map[string]string, 0)
	for k, v := range proj.Env {
		out = append(out, map[string]string{"key": k, "value": v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["key"] < out[j]["key"] })
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSetEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if proj.Env == nil {
		proj.Env = map[string]string{}
	}
	proj.Env[req.Key] = req.Value
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"key": req.Key, "value": req.Value})
}

func (a *API) handleSetEnvBulk(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if proj.Env == nil {
		proj.Env = map[string]string{}
	}
	for k, v := range req {
		proj.Env[k] = v
	}
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"set": len(req)})
}

func (a *API) handlePatchEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
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
	if proj.Env != nil {
		proj.Env[r.PathValue("envId")] = req.Value
		a.store.PutProject(proj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": r.PathValue("envId"), "value": req.Value})
}

func (a *API) handleDeleteEnv(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	delete(proj.Env, r.PathValue("envId"))
	a.store.PutProject(proj)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	projID := a.projectID(r)
	if rows, err := a.store.ListSecretsTx(r.Context(), projID, projID); err == nil {
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{"id": row["id"], "name": row["name"], "value": "••••••"})
		}
		writeJSON(w, http.StatusOK, selectFields(r, out))
		return
	}
	secrets := a.store.ListSecrets(projID)
	out := make([]map[string]any, 0, len(secrets))
	for _, s := range secrets {
		out = append(out, map[string]any{"id": s.ID, "name": s.Name, "value": "••••••", "created_at": s.CreatedAt})
	}
	writeJSON(w, http.StatusOK, selectFields(r, out))
}

func (a *API) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if a.secretKeyMaterial == "" {
		writeError(w, http.StatusServiceUnavailable, "project secret encryption is not configured; set PORTER_SECRET_KEY")
		return
	}
	enc, err := a.encryptSecret(req.Value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt secret: "+err.Error())
		return
	}
	sec := &types.Secret{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, ValueEncrypted: enc, CreatedAt: time.Now()}
	if err := a.store.PutSecret(sec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": sec.ID, "name": sec.Name})
}

func (a *API) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteSecret(r.PathValue("secretId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// Secret versioning + rotation (SRS §26)

func (a *API) handleListSecretVersions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /projects/{projectId}/secrets/{secretId}/versions")
}

func (a *API) handleRotateSecret(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /projects/{projectId}/secrets/{secretId}/rotate")
}

func (a *API) handleRollbackSecret(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /projects/{projectId}/secrets/{secretId}/rollback/{version}")
}

func (a *API) secretKey() []byte { return secretbox.Key(a.secretKeyMaterial) }

func (a *API) encryptSecret(value string) ([]byte, error) {
	return secretbox.Seal(a.secretKey(), []byte(value))
}

func (a *API) decryptSecret(blob []byte) (string, error) {
	raw, err := secretbox.Open(a.secretKey(), blob)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (a *API) projectSecret(projectID, name string) (string, error) {
	for _, s := range a.store.ListSecrets(projectID) {
		if s == nil || s.Name != name {
			continue
		}
		if a.secretKeyMaterial == "" {
			return "", fmt.Errorf("project secret encryption is not configured; set PORTER_SECRET_KEY")
		}
		return a.decryptSecret(s.ValueEncrypted)
	}
	return "", fmt.Errorf("project secret %q not found (store it via POST /projects/{id}/secrets)", name)
}

func (a *API) putProjectSecret(projectID, name, value string) error {
	if a.secretKeyMaterial == "" {
		return fmt.Errorf("project secret encryption is not configured; set PORTER_SECRET_KEY")
	}
	for _, s := range a.store.ListSecrets(projectID) {
		if s != nil && s.Name == name {
			_ = a.store.DeleteSecret(s.ID)
		}
	}
	enc, err := a.encryptSecret(value)
	if err != nil {
		return err
	}
	return a.store.PutSecret(&types.Secret{ID: store.NewID(), ProjectID: projectID, Name: name, ValueEncrypted: enc, CreatedAt: time.Now()})
}

// Shared variables (team / project / environment / server)

func (a *API) handleListTeamEnv(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /team/envs")
}

func (a *API) handleCreateTeamEnv(w http.ResponseWriter, r *http.Request) {
	a.createSharedEnv(w, r, "team", "")
}

func (a *API) handlePatchTeamEnv(w http.ResponseWriter, r *http.Request) {
	a.patchSharedEnv(w, r, "team", "", r.PathValue("envId"))
}

func (a *API) handleDeleteTeamEnv(w http.ResponseWriter, r *http.Request) {
	a.deleteSharedEnv(w, r, "team", "", r.PathValue("envId"))
}

func (a *API) handleListProjectSharedEnv(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /projects/{projectId}/envs")
}

func (a *API) handleCreateProjectSharedEnv(w http.ResponseWriter, r *http.Request) {
	a.createSharedEnv(w, r, "project", a.projectID(r))
}

func (a *API) handlePatchProjectSharedEnv(w http.ResponseWriter, r *http.Request) {
	a.patchSharedEnv(w, r, "project", a.projectID(r), r.PathValue("envId"))
}

func (a *API) handleDeleteProjectSharedEnv(w http.ResponseWriter, r *http.Request) {
	a.deleteSharedEnv(w, r, "project", a.projectID(r), r.PathValue("envId"))
}

func (a *API) handleListEnvSharedEnv(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /projects/{projectId}/environments/{envId}/envs")
}

func (a *API) handleCreateEnvSharedEnv(w http.ResponseWriter, r *http.Request) {
	a.createSharedEnv(w, r, "environment", r.PathValue("envId"))
}

func (a *API) createSharedEnv(w http.ResponseWriter, r *http.Request, scope, scopeID string) {
	notImplemented(w, "POST /shared-env ("+scope+" scope)")
}

func (a *API) patchSharedEnv(w http.ResponseWriter, r *http.Request, scope, scopeID, envID string) {
	notImplemented(w, "PATCH /shared-env/"+envID+" ("+scope+" scope)")
}

func (a *API) deleteSharedEnv(w http.ResponseWriter, r *http.Request, scope, scopeID, envID string) {
	notImplemented(w, "DELETE /shared-env/"+envID+" ("+scope+" scope)")
}
