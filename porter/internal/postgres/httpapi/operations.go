package httpapi

import (
	"net/http"
	"net/url"

	v1 "porter/controlplane/contract/v1"
)

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "operations", "")
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "operations", "")
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListOperationsPage(r.Context(), pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Operation]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("operations", "", last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "operation detail does not accept query parameters")
		return
	}
	operation, err := s.repository.GetOperation(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Content-Location", v1.BasePath+"/operations/"+url.PathEscape(operation.ID))
	writeJSON(w, http.StatusOK, operation)
}
