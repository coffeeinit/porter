// Orgs, teams, members, users, roles/RBAC, resellers, customers.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"porter/internal/store"
	"porter/internal/types"
)

func (a *API) handlePasswordForgot(w http.ResponseWriter, r *http.Request) {
	a.store.AppendDaemonLog("password reset requested; no recovery provider configured")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "unsupported",
		"reason": "password recovery provider is not configured; an authorized operator must rotate the database credential",
	})
}

// ============================================================================
// Orgs / Resellers / Customers
// ============================================================================

func (a *API) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListOrgs())
}

func (a *API) handleListResellers(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /resellers")
}

func (a *API) handleDefaultOrg(w http.ResponseWriter, r *http.Request) {
	if org, ok := a.store.GetOrg(a.orgIDFromHeader(r)); ok {
		writeJSON(w, http.StatusOK, org)
		return
	}
	if orgs := a.store.ListOrgs(); len(orgs) > 0 {
		writeJSON(w, http.StatusOK, orgs[0])
		return
	}
	writeError(w, http.StatusNotFound, "no org set")
}

func (a *API) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "org name is required")
		return
	}
	org := &types.Org{ID: store.NewID(), Name: req.Name, OwnerID: a.userIDFromHeader(r), IsDefault: true, CreatedAt: time.Now()}
	if err := a.store.PutOrg(org); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create org: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (a *API) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	if org, ok := a.store.GetOrg(a.orgIDFromHeader(r)); ok {
		writeJSON(w, http.StatusOK, org)
		return
	}
	writeError(w, http.StatusNotFound, "org not found")
}

func (a *API) handlePatchOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	org, ok := a.store.GetOrg(a.orgIDFromHeader(r))
	if !ok {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}
	if req.Name != "" {
		org.Name = req.Name
	}
	if err := a.store.PutOrg(org); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update org: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (a *API) handleCreateReseller(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /resellers")
}

func (a *API) handleGetReseller(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /resellers/{id}")
}

func (a *API) handlePatchReseller(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /resellers/{id}")
}

func (a *API) handleDeleteReseller(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /resellers/{id}")
}

func (a *API) handleListResellerCustomers(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /resellers/{id}/customers")
}

func (a *API) handleAddResellerCustomer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /resellers/{id}/customers")
}

func (a *API) handleListCustomers(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /customers")
}

func (a *API) handleCreateCustomer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /customers")
}

func (a *API) handleGetCustomer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /customers/{id}")
}

func (a *API) handlePatchCustomer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /customers/{id}")
}

func (a *API) handleDeleteCustomer(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /customers/{id}")
}

func (a *API) ensurePersonalTeam(r *http.Request, u *types.User) {
	if u == nil {
		return
	}
	orgID := a.orgIDFromHeader(r)
	if orgID == "" {
		return
	}
	name := u.Username + "-team"
	for _, g := range a.store.ListGroups(orgID) {
		if g.Name == name {
			return
		}
	}
	g := &types.Group{ID: store.NewID(), OrgID: orgID, Name: name, CreatedAt: time.Now()}
	if err := a.store.PutGroup(g); err != nil {
		return
	}
	role := a.store.DefaultRoleID()
	if role == "" {
		return
	}
	_ = a.store.AddTeamMember(g.ID, u.ID, role)
}

// Team admin / danger / audit / invitations

func (a *API) handleTeamAdmin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"org_id": a.orgIDFromHeader(r), "admin": true})
}

func (a *API) handleTeamDanger(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"org_id": a.orgIDFromHeader(r), "actions": []string{"transfer", "delete"}})
}

func (a *API) handleGetInvitation(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /invitations/{uuid}")
}

func (a *API) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /invitations/{uuid}/accept")
}

func (a *API) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /invitations/{uuid}/revoke")
}

// --- RECOVERED 2026-10: bodies dropped in the feature_*.go -> handlers.go
// --- consolidation; extracted verbatim from git HEAD. Merge in place later.
// recovered from internal/api/handlers.go
func (a *API) handleAddRolePermission(w http.ResponseWriter, r *http.Request) {
	if a.systemRole(r.PathValue("roleId")) {
		writeError(w, http.StatusForbidden, "system role permissions are migration-managed")
		return
	}
	if _, ok := a.store.GetRole(r.PathValue("roleId")); !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	a.store.AddRolePermission(r.PathValue("roleId"), r.PathValue("permissionId"))
	writeJSON(w, http.StatusOK, map[string]any{"status": "granted"})
}

// recovered from internal/api/feature_aux.go
func (a *API) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil || req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role != "admin" {
		req.Role = "member" // teams grant member; admin stays org-level
	}
	user, ok := a.store.GetUserByUsername(req.Username)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := a.store.AddTeamMember(r.PathValue("groupId"), user.ID, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"username": req.Username, "role": req.Role})
}

// recovered from internal/api/feature_bootstrap.go
func (a *API) handleAssignRBACRole(w http.ResponseWriter, r *http.Request) {
	var req rbacAssignmentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid assignment body")
		return
	}
	if req.PrincipalType == "" {
		req.PrincipalType = "user"
	}
	if req.PrincipalType != "user" {
		writeError(w, http.StatusBadRequest, "principal_type must be user")
		return
	}
	if req.PrincipalID == "" {
		writeError(w, http.StatusBadRequest, "principal_id is required")
		return
	}
	if !rbacScopeTypes[req.ScopeType] {
		writeError(w, http.StatusBadRequest, "scope_type must be platform|org|project")
		return
	}
	if req.ScopeID == "" && req.ScopeType != "platform" {
		writeError(w, http.StatusBadRequest, "scope_id is required for org/project scopes")
		return
	}
	if req.RoleID == "" {
		writeError(w, http.StatusBadRequest, "role_id is required")
		return
	}
	if _, ok := a.store.GetRole(req.RoleID); !ok {
		writeError(w, http.StatusBadRequest, "unknown role: "+req.RoleID)
		return
	}
	if strings.EqualFold(req.PrincipalType, "user") {
		if _, ok := a.store.GetUserByUsername(req.PrincipalID); !ok {
			writeError(w, http.StatusNotFound, "user not found: "+req.PrincipalID)
			return
		}
	}
	if err := a.store.AssignRole(req.PrincipalType, req.PrincipalID, req.ScopeType, req.ScopeID, req.RoleID, currentUser(r)); err != nil {
		writeError(w, http.StatusInternalServerError, "assign role: "+err.Error())
		return
	}
	a.store.AppendDaemonLog(fmt.Sprintf("rbac: %s granted %s at %s/%s by %s",
		req.PrincipalID, req.RoleID, req.ScopeType, req.ScopeID, currentUser(r)))
	writeJSON(w, http.StatusCreated, map[string]any{
		"principal_type": req.PrincipalType, "principal_id": req.PrincipalID,
		"scope_type": req.ScopeType, "scope_id": req.ScopeID, "role_id": req.RoleID,
	})
}

// recovered from internal/api/handlers.go
func (a *API) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var req types.Role
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "role id and name are required")
		return
	}
	a.store.PutRole(&req)
	if len(req.Permissions) > 0 {
		_ = a.store.SetRolePermissions(req.ID, req.Permissions)
	}
	role, _ := a.store.GetRole(req.ID)
	writeJSON(w, http.StatusCreated, role)
}

// recovered from internal/api/handlers.go
func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Email       string `json:"email"`
		NotifyOptIn bool   `json:"notify_opt_in"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role == "" {
		req.Role = a.store.DefaultRoleID()
	}
	if req.Role == "" {
		writeError(w, http.StatusServiceUnavailable, "no default role is seeded; create one via POST /roles")
		return
	}
	if _, ok := a.store.GetRole(req.Role); !ok {
		writeError(w, http.StatusBadRequest, "unknown role: "+req.Role)
		return
	}
	salt := store.NewID()
	u := &types.User{ID: store.NewID(), Username: req.Username, Role: req.Role, Email: req.Email, NotifyOptIn: req.NotifyOptIn, Salt: salt, PasswordHash: passwordHash(req.Password, salt), CreatedAt: time.Now()}
	a.store.PutUser(u)
	// Railway-style onboarding: every user gets a personal team they own, so
	// team → project sharing works from the first login. Best-effort: user
	// creation never fails when team setup cannot complete.
	a.ensurePersonalTeam(r, u)
	writeJSON(w, http.StatusCreated, u)
}

// recovered from internal/api/handlers.go
func (a *API) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	if a.systemRole(r.PathValue("roleId")) {
		writeError(w, http.StatusForbidden, "system roles cannot be deleted")
		return
	}
	if a.store.DeleteRole(r.PathValue("roleId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "role not found")
}

// !! handleGetDisruptionBudget NOT AT HEAD (new-surface handler, needs writing)
// recovered from internal/api/handlers.go
func (a *API) handleGetRole(w http.ResponseWriter, r *http.Request) {
	if role, ok := a.store.GetRole(r.PathValue("roleId")); ok {
		writeJSON(w, http.StatusOK, role)
		return
	}
	writeError(w, http.StatusNotFound, "role not found")
}

// recovered from internal/api/handlers.go
func (a *API) handleGetRolePermissions(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.store.GetRole(r.PathValue("roleId")); !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":        r.PathValue("roleId"),
		"permissions": a.store.RolePermissions(r.PathValue("roleId")),
	})
}

// recovered from internal/api/handlers.go
func (a *API) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListAllPermissions())
}

// recovered from internal/api/handlers.go
func (a *API) handleListProjectMembers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListProjectMembers(a.projectID(r)))
}

// recovered from internal/api/feature_bootstrap.go
func (a *API) handleListRBACAssignments(w http.ResponseWriter, r *http.Request) {
	scopeType, scopeID := r.URL.Query().Get("scope_type"), r.URL.Query().Get("scope_id")
	if !rbacScopeTypes[scopeType] {
		writeError(w, http.StatusBadRequest, "scope_type must be platform|org|project")
		return
	}
	if scopeID == "" && scopeType != "platform" {
		writeError(w, http.StatusBadRequest, "scope_id is required for org/project scopes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope_type": scopeType, "scope_id": scopeID,
		"assignments": a.store.ListAssignmentsByScope(scopeType, scopeID),
	})
}

// recovered from internal/api/handlers.go
func (a *API) handleListRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListRoles())
}

// recovered from internal/api/feature_aux.go
func (a *API) handleListTeamMembers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"members": a.store.ListTeamMembers(r.PathValue("groupId"))})
}

// recovered from internal/api/handlers.go
func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListUsers())
}

// recovered from internal/api/handlers.go
func (a *API) handlePatchRole(w http.ResponseWriter, r *http.Request) {
	if a.systemRole(r.PathValue("roleId")) {
		writeError(w, http.StatusForbidden, "system roles cannot be edited")
		return
	}
	role, ok := a.store.GetRole(r.PathValue("roleId"))
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name != "" {
		role.Name = req.Name
	}
	if req.Description != "" {
		role.Description = req.Description
	}
	a.store.PutRole(role)
	writeJSON(w, http.StatusOK, role)
}

// !! handlePutDisruptionBudget NOT AT HEAD (new-surface handler, needs writing)
// recovered from internal/api/handlers.go
func (a *API) handleRemoveRolePermission(w http.ResponseWriter, r *http.Request) {
	if a.systemRole(r.PathValue("roleId")) {
		writeError(w, http.StatusForbidden, "system role permissions are migration-managed")
		return
	}
	if _, ok := a.store.GetRole(r.PathValue("roleId")); !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	a.store.RemoveRolePermission(r.PathValue("roleId"), r.PathValue("permissionId"))
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// recovered from internal/api/feature_aux.go
func (a *API) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	if err := a.store.RemoveTeamMember(r.PathValue("groupId"), r.PathValue("username")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

// recovered from internal/api/feature_bootstrap.go
func (a *API) handleRevokeRBACRole(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pt, pid := q.Get("principal_type"), q.Get("principal_id")
	st, sid, role := q.Get("scope_type"), q.Get("scope_id"), q.Get("role_id")
	if pt == "" || pid == "" || st == "" || role == "" {
		writeError(w, http.StatusBadRequest, "principal_type, principal_id, scope_type and role_id are required")
		return
	}
	if !rbacScopeTypes[st] {
		writeError(w, http.StatusBadRequest, "scope_type must be platform|org|project")
		return
	}
	if err := a.store.RevokeRole(pt, pid, st, sid, role); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke role: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// recovered from internal/api/handlers.go
func (a *API) handleSetRolePermissions(w http.ResponseWriter, r *http.Request) {
	if a.systemRole(r.PathValue("roleId")) {
		writeError(w, http.StatusForbidden, "system role permissions are migration-managed")
		return
	}
	if _, ok := a.store.GetRole(r.PathValue("roleId")); !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	var req struct {
		Permissions []string `json:"permissions"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if err := a.store.SetRolePermissions(r.PathValue("roleId"), req.Permissions); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update permissions: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":        r.PathValue("roleId"),
		"permissions": a.store.RolePermissions(r.PathValue("roleId")),
	})
}

// from feature_bootstrap.go
type rbacAssignmentReq struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	RoleID        string `json:"role_id"`
}

// from feature_bootstrap.go
var rbacScopeTypes = map[string]bool{"platform": true, "org": true, "project": true}
