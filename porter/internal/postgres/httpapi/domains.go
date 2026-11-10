package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

func (s *Server) handleDomainClaims(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	s.listDomainClaims(w, r, "", "")
}

func (s *Server) handleProjectServiceDomainClaims(w http.ResponseWriter, r *http.Request, projectID, serviceID string) {
	switch r.Method {
	case http.MethodGet:
		s.listDomainClaims(w, r, projectID, serviceID)
	case http.MethodPost:
		s.createProjectServiceDomainClaim(w, r, projectID, serviceID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listDomainClaims(w http.ResponseWriter, r *http.Request, projectID, serviceID string) {
	if projectID != "" {
		service, err := s.repository.GetService(r.Context(), serviceID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		if service.ProjectID != projectID {
			writeError(w, http.StatusNotFound, v1.ErrorNotFound, "resource not found")
			return
		}
	}
	scope := ""
	if projectID != "" {
		scope = projectID + "/" + serviceID
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "domain-claims", scope)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "domain-claims", scope)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListDomainClaimsPage(r.Context(), projectID, serviceID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.DomainClaim]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("domain-claims", scope, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createProjectServiceDomainClaim(w http.ResponseWriter, r *http.Request, projectID, serviceID string) {
	var request v1.CreateDomainClaimRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	request.Hostname = strings.TrimSpace(request.Hostname)
	if request.Hostname == "" {
		badPageQuery(w, "hostname is required")
		return
	}
	service, err := s.repository.GetService(r.Context(), serviceID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if service.ProjectID != projectID {
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "resource not found")
		return
	}
	claim, err := s.repository.CreateDomainClaim(r.Context(), projectID, serviceID, request.Hostname)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/domain-claims/"+url.PathEscape(claim.ID))
	writeJSON(w, http.StatusCreated, claim)
}

func (s *Server) handleDomainClaim(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	claim, err := s.repository.GetDomainClaim(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (s *Server) verifyDomainClaimTXT(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request struct{}
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	claim, err := s.repository.GetDomainClaim(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	lookupName := claim.VerificationTXTName + "."
	values, err := s.txtResolver.LookupTXT(r.Context(), lookupName)
	if err != nil {
		var dnsError *net.DNSError
		if !errors.As(err, &dnsError) || !dnsError.IsNotFound {
			writeError(w, http.StatusServiceUnavailable, v1.ErrorInternal, "DNS TXT lookup is unavailable; no proof was recorded")
			return
		}
	}
	matched := false
	for _, value := range values {
		if value == claim.VerificationTXTValue {
			matched = true
			break
		}
	}
	if !matched {
		writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition,
			"TXT ownership proof was not found; publish the supplied TXT record and retry")
		return
	}
	verified, err := s.repository.RecordDomainTXTProof(r.Context(), id, claim.VerificationTXTValue)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, verified)
}

func (s *Server) handleDomainClaimRoutes(w http.ResponseWriter, r *http.Request, claimID string) {
	switch r.Method {
	case http.MethodGet:
		s.listDomainClaimRoutes(w, r, claimID)
	case http.MethodPost:
		s.createDomainClaimRoute(w, r, claimID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listDomainClaimRoutes(w http.ResponseWriter, r *http.Request, claimID string) {
	if _, err := s.repository.GetDomainClaim(r.Context(), claimID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "gateway-routes", claimID)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "gateway-routes", claimID)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListGatewayRoutesPage(r.Context(), claimID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.GatewayRoute]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("gateway-routes", claimID, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createDomainClaimRoute(w http.ResponseWriter, r *http.Request, claimID string) {
	var request v1.CreateGatewayRouteRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	if request.PathPrefix == "" {
		request.PathPrefix = "/"
	}
	route, err := s.repository.CreateGatewayRoute(r.Context(), claimID, request.PathPrefix)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/gateway-routes/"+url.PathEscape(route.ID))
	writeJSON(w, http.StatusCreated, route)
}

func (s *Server) handleGatewayRoute(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		route, err := s.repository.GetGatewayRoute(r.Context(), id)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, route)
	case http.MethodPatch:
		var request v1.SetGatewayRouteRequest
		if err := decodeJSONBody(w, r, &request); err != nil || request.DesiredEnabled == nil {
			bodyError(w)
			return
		}
		route, err := s.repository.SetGatewayRouteDesiredEnabled(r.Context(), id, *request.DesiredEnabled)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, route)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}
