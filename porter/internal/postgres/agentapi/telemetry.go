package agentapi

import (
	"net/http"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "telemetry ingestion does not accept query parameters")
		return
	}
	nodeID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "Content-Type must be application/json")
		return
	}
	var batch protocol.TelemetryBatchV2
	if err := decodeStrictJSON(w, r, &batch); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid v2 telemetry batch")
		return
	}
	if err := batch.ValidateAt(s.now().UTC()); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid or stale v2 telemetry batch")
		return
	}
	if batch.NodeID != nodeID {
		writeError(w, http.StatusForbidden, v1.ErrorPermissionDenied, "telemetry node does not match the authenticated node")
		return
	}
	if _, err := s.repository.IngestAgentTelemetry(r.Context(), nodeID, batch, s.now().UTC()); err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
