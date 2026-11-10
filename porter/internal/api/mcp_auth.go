package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"porter/controlplane/internal/store"
)

type mcpPrincipalContextKey struct{}

type mcpPrincipal struct {
	ID           string
	Name         string
	Admin        bool
	Capabilities map[string]bool
	ProjectIDs   []string
	AppIDs       []string
	NodeIDs      []string
}

func (p mcpPrincipal) hasCapability(capability string) bool {
	return p.Admin || p.Capabilities[capability]
}

func (p mcpPrincipal) hasScope(scopes []string, id string) bool {
	for _, scope := range scopes {
		if scope == "*" || scope == id {
			return true
		}
	}
	return false
}

func principalFromContext(ctx context.Context) mcpPrincipal {
	p, _ := ctx.Value(mcpPrincipalContextKey{}).(mcpPrincipal)
	return p
}

func (s *Server) mcpAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1 {
			p := mcpPrincipal{ID: "admin", Name: "operator", Admin: true}
			next(w, r.WithContext(context.WithValue(r.Context(), mcpPrincipalContextKey{}, p)))
			return
		}
		credential, ok, err := s.Store.AuthenticateMCPToken(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "MCP credential lookup failed")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "valid admin or scoped MCP token required")
			return
		}
		p := mcpPrincipal{
			ID: credential.ID, Name: credential.Name,
			Capabilities: make(map[string]bool, len(credential.Capabilities)),
			ProjectIDs:   credential.ProjectIDs, AppIDs: credential.AppIDs, NodeIDs: credential.NodeIDs,
		}
		for _, capability := range credential.Capabilities {
			p.Capabilities[capability] = true
		}
		next(w, r.WithContext(context.WithValue(r.Context(), mcpPrincipalContextKey{}, p)))
	}
}

var knownMCPCapabilities = map[string]struct{}{
	"projects:read": {}, "projects:create": {},
	"apps:read": {}, "apps:create": {}, "apps:deploy": {}, "apps:scale": {}, "apps:configure": {}, "apps:operate": {},
	"fleet:read": {}, "fleet:write": {},
	"operations:read": {}, "volumes:read": {}, "volumes:create": {}, "snapshots:read": {}, "snapshots:create": {},
	"logs:read": {}, "metrics:read": {}, "domains:read": {}, "domains:write": {}, "gateway:read": {},
	"images:read": {}, "images:build": {}, "images:write": {},
}

type mcpToolRule struct {
	Capability string
	Resource   string
	AdminOnly  bool
}

// This closed policy table is the active Porter MCP authorization boundary.
// Unknown tools are denied, including tools added to the catalog without a rule.
var mcpToolRules = map[string]mcpToolRule{
	"porter_list_nodes":         {"fleet:read", "node_list", false},
	"porter_get_node":           {"fleet:read", "node", false},
	"porter_node_metrics":       {"fleet:read", "node", false},
	"porter_node_logs":          {"fleet:read", "node", false},
	"porter_node_vms":           {"fleet:read", "node", false},
	"porter_cordon_node":        {"fleet:write", "node", true},
	"porter_uncordon_node":      {"fleet:write", "node", true},
	"porter_drain_node":         {"fleet:write", "node", true},
	"porter_list_projects":      {"projects:read", "project_list", false},
	"porter_create_project":     {"projects:create", "project_create", false},
	"porter_create_app":         {"apps:create", "project", false},
	"porter_deploy":             {"apps:deploy", "app", false},
	"porter_scale_app":          {"apps:scale", "app", false},
	"porter_list_operations":    {"operations:read", "global", true},
	"porter_list_volumes":       {"volumes:read", "project", false},
	"porter_create_volume":      {"volumes:create", "project", false},
	"porter_attach_volume":      {"volumes:create", "global", true},
	"porter_list_snapshots":     {"snapshots:read", "all_apps", false},
	"porter_create_snapshot":    {"snapshots:create", "all_apps", true},
	"porter_list_logs":          {"logs:read", "all_apps", false},
	"porter_list_metrics":       {"metrics:read", "global", true},
	"porter_get_autoscale":      {"apps:read", "app", false},
	"porter_set_autoscale":      {"apps:configure", "app", false},
	"porter_touch_app":          {"apps:operate", "app", false},
	"porter_wake_app":           {"apps:operate", "app", false},
	"porter_list_domains":       {"domains:read", "domain_list", false},
	"porter_create_domain":      {"domains:write", "project", true},
	"porter_gateway_stats":      {"gateway:read", "global", true},
	"porter_gateway_requests":   {"gateway:read", "global", true},
	"porter_list_traces":        {"gateway:read", "global", true},
	"porter_create_preview":     {"domains:write", "app", true},
	"porter_list_images":        {"images:read", "global", true},
	"porter_build_image":        {"images:build", "global", true},
	"porter_detect_framework":   {"images:build", "global", true},
	"porter_create_image":       {"images:write", "global", true},
	"porter_resize_volume":      {"volumes:create", "global", true},
	"porter_set_replica_weight": {"apps:operate", "global", true},
	"porter_restore_snapshot":   {"snapshots:create", "global", true},
}

func visibleMCPTools(principal mcpPrincipal) []map[string]interface{} {
	all := porterMCPTools()
	if principal.Admin {
		visible := make([]map[string]interface{}, 0, len(all))
		for _, tool := range all {
			if name, ok := tool["name"].(string); ok {
				if _, ruled := mcpToolRules[name]; ruled {
					visible = append(visible, tool)
				}
			}
		}
		return visible
	}
	visible := make([]map[string]interface{}, 0, len(all))
	for _, tool := range all {
		name, _ := tool["name"].(string)
		rule, ok := mcpToolRules[name]
		if ok && !rule.AdminOnly && principal.hasCapability(rule.Capability) {
			visible = append(visible, tool)
		}
	}
	return visible
}

func (s *Server) authorizeMCPTool(ctx context.Context, principal mcpPrincipal, name string, raw json.RawMessage) error {
	rule, ok := mcpToolRules[name]
	if !ok {
		return &mcpAuthorizationError{message: "tool has no authorization policy"}
	}
	if principal.Admin {
		return nil
	}
	if rule.AdminOnly {
		return &mcpAuthorizationError{message: "tool is operator-only until an approval workflow is available"}
	}
	if !principal.hasCapability(rule.Capability) {
		return &mcpAuthorizationError{message: "missing capability: " + rule.Capability}
	}
	var args map[string]json.RawMessage
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
	}
	arg := func(key string) string {
		var value string
		_ = json.Unmarshal(args[key], &value)
		return value
	}
	switch rule.Resource {
	case "global":
		return nil
	case "project_list":
		if len(principal.ProjectIDs) == 0 {
			return &mcpAuthorizationError{message: "project scope is required"}
		}
		if projectID := arg("project_id"); projectID != "" && !principal.hasScope(principal.ProjectIDs, projectID) {
			return &mcpAuthorizationError{message: "project is outside this token's scope"}
		}
		return nil
	case "domain_list":
		if len(principal.ProjectIDs) == 0 {
			return &mcpAuthorizationError{message: "project scope is required"}
		}
		if projectID := arg("project_id"); projectID == "" {
			if !principal.hasScope(principal.ProjectIDs, "*") {
				return &mcpAuthorizationError{message: "project_id is required unless this token has wildcard project scope"}
			}
		} else if !principal.hasScope(principal.ProjectIDs, projectID) {
			return &mcpAuthorizationError{message: "project is outside this token's scope"}
		}
		return nil
	case "project_create":
		if !principal.hasScope(principal.ProjectIDs, "*") {
			return &mcpAuthorizationError{message: "creating projects requires wildcard project scope"}
		}
		return nil
	case "project":
		projectID := arg("project_id")
		if projectID == "" || !principal.hasScope(principal.ProjectIDs, projectID) {
			return &mcpAuthorizationError{message: "project is outside this token's scope"}
		}
		return nil
	case "app":
		appID := arg("app_id")
		if appID == "" || !s.mcpAppInScope(ctx, principal, appID) {
			return &mcpAuthorizationError{message: "app is outside this token's scope"}
		}
		return nil
	case "all_apps":
		if !principal.hasScope(principal.AppIDs, "*") && !principal.hasScope(principal.ProjectIDs, "*") {
			return &mcpAuthorizationError{message: "this operation requires wildcard app or project scope; arbitrary VM identifiers cannot be authorized from narrow scopes"}
		}
		if (name == "porter_list_snapshots" || name == "porter_list_logs") && arg("vm_id") == "" {
			return &mcpAuthorizationError{message: "vm_id is required"}
		}
		return nil
	case "node":
		nodeID := arg("node_id")
		if nodeID == "" || !principal.hasScope(principal.NodeIDs, nodeID) {
			return &mcpAuthorizationError{message: "node is outside this token's scope"}
		}
		return nil
	case "node_list":
		if len(principal.NodeIDs) == 0 {
			return &mcpAuthorizationError{message: "node scope is required"}
		}
		return nil
	default:
		return &mcpAuthorizationError{message: "unsupported authorization scope"}
	}
}

func (s *Server) mcpAppInScope(ctx context.Context, principal mcpPrincipal, appID string) bool {
	if principal.hasScope(principal.AppIDs, appID) {
		return true
	}
	if principal.hasScope(principal.ProjectIDs, "*") {
		return true
	}
	app, ok := s.Store.GetApp(ctx, appID)
	return ok && principal.hasScope(principal.ProjectIDs, app.ProjectID)
}

type mcpAuthorizationError struct{ message string }

func (e *mcpAuthorizationError) Error() string { return e.message }

func (s *Server) listMCPTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.Store.ListMCPTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) createMCPToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string     `json:"name"`
		Capabilities []string   `json:"capabilities"`
		ProjectIDs   []string   `json:"project_ids"`
		AppIDs       []string   `json:"app_ids"`
		NodeIDs      []string   `json:"node_ids"`
		ExpiresAt    *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.Capabilities) == 0 {
		writeError(w, http.StatusBadRequest, "at least one capability is required")
		return
	}
	for _, capability := range req.Capabilities {
		if _, ok := knownMCPCapabilities[capability]; !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown capability: %s", capability))
			return
		}
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusBadRequest, "expires_at must be in the future")
		return
	}
	record, token, err := s.Store.CreateMCPToken(r.Context(), req.Name, req.Capabilities, req.ProjectIDs, req.AppIDs, req.NodeIDs, req.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"token": token, "credential": record})
}

func (s *Server) revokeMCPToken(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RevokeMCPToken(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrMCPTokenNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
