// AI agents/plans, MCP, impersonation.
package api

import (
	"net/http"

	"porter/internal/ai"
)

// ============================================================================
// AI / MCP / impersonation
// ============================================================================

func (a *API) handleListAIAgents(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /ai/agents")
}

func (a *API) handleCreateAIAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /ai/agents")
}

func (a *API) handleGetAIAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /ai/agents/{id}")
}

func (a *API) handlePatchAIAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /ai/agents/{id}")
}

func (a *API) handleDeleteAIAgent(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /ai/agents/{id}")
}

func (a *API) handleCreateAIPlan(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /ai/agents/{id}/plans")
}

func (a *API) handleGetAIPlan(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /ai/plans/{id}")
}

func (a *API) handleApproveAIPlan(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /ai/plans/{id}/approve")
}

func (a *API) handleRejectAIPlan(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /ai/plans/{id}/reject")
}

func (a *API) mcpServer() *ai.Server {
	if a.mcp == nil {
		a.mcp = ai.NewServer(func(actor, address, decision, errMsg string) {
			a.store.AppendDaemonLog("mcp " + decision + " actor=" + actor + " " + address + " " + errMsg)
		})
	}
	return a.mcp
}

type mcpSearchReq struct {
	Workspace string `json:"workspace"`
	Query     string `json:"q"`
}

func (a *API) handleMCPSearch(w http.ResponseWriter, r *http.Request) {
	var req mcpSearchReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": a.mcpServer().Search(req.Workspace, req.Query)})
}

func (a *API) handleMCPDescribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := readJSON(r, &req); err != nil || req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	d, err := a.mcpServer().Describe(req.Address)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *API) handleMCPCall(w http.ResponseWriter, r *http.Request) {
	var call ai.Call
	if err := readJSON(r, &call); err != nil || call.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	res, err := a.mcpServer().Call(currentUser(r), call)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (a *API) handleImpersonate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /support/impersonate")
}

func (a *API) handleEndImpersonate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /support/impersonate/end")
}
