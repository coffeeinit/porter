package api

import (
	"errors"
	"net/http"

	"porter/controlplane/internal/store"
)

func (s *Server) replicaLifecycle(w http.ResponseWriter, r *http.Request) {
	opID, err := s.Store.QueueReplicaLifecycle(r.Context(), r.PathValue("id"), r.PathValue("action"))
	if err != nil {
		switch {
		case errors.Is(err, store.ErrReplicaNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, store.ErrReplicaNotScheduled),
			errors.Is(err, store.ErrReplicaOperationInProgress),
			errors.Is(err, store.ErrReplicaLifecycleStateConflict),
			errors.Is(err, store.ErrReplicaApplySpecNotFound):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrInvalidReplicaLifecycleAction):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":       "accepted",
		"operation_id": opID,
	})
}
