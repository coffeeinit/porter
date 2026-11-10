package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/buildservice"
)

const maxRequestBodyBytes = 1 << 20

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listProjects(w, r)
	case http.MethodPost:
		s.createProject(w, r)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	pageSize, cursorText, ok := s.timePageParams(w, r, "projects", "")
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "projects", "")
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListProjectsPage(r.Context(), pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Project]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("projects", "", last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var request v1.CreateProjectRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if !validName(request.Name) {
		badPageQuery(w, "project name must contain 1 to 128 characters")
		return
	}
	project, err := s.repository.CreateProject(r.Context(), request.Name)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/projects/"+url.PathEscape(project.ID))
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	project, err := s.repository.GetProject(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) handleProjectServices(w http.ResponseWriter, r *http.Request, projectID string) {
	switch r.Method {
	case http.MethodGet:
		s.listProjectServices(w, r, projectID)
	case http.MethodPost:
		s.createProjectService(w, r, projectID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listProjectServices(w http.ResponseWriter, r *http.Request, projectID string) {
	if _, err := s.repository.GetProject(r.Context(), projectID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "services", projectID)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "services", projectID)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListServicesPage(r.Context(), projectID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Service]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("services", projectID, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createProjectService(w http.ResponseWriter, r *http.Request, projectID string) {
	var request v1.CreateServiceRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if !validName(request.Name) {
		badPageQuery(w, "service name must contain 1 to 128 characters")
		return
	}
	if request.Port < 0 || request.Port > 65535 || request.VCPUs < 0 || request.MemoryMB < 0 || request.DesiredInstances < 0 {
		badPageQuery(w, "service resource values must be non-negative and port cannot exceed 65535")
		return
	}
	if request.Runtime != "" && request.Runtime != v1.FirecrackerRuntime {
		badPageQuery(w, "production service runtime must be firecracker")
		return
	}
	service, err := s.repository.CreateService(r.Context(), projectID, request)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Location", v1.BasePath+"/services/"+url.PathEscape(service.ID))
	writeJSON(w, http.StatusCreated, service)
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	service, err := s.repository.GetService(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, service)
}

func (s *Server) handleServiceReleases(w http.ResponseWriter, r *http.Request, serviceID string) {
	switch r.Method {
	case http.MethodGet:
		s.listServiceReleases(w, r, serviceID)
	case http.MethodPost:
		s.createRelease(w, r, serviceID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) listServiceReleases(w http.ResponseWriter, r *http.Request, serviceID string) {
	if _, err := s.repository.GetService(r.Context(), serviceID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "releases", serviceID)
	if !ok {
		return
	}
	cursor, err := s.decodeReleaseCursor(cursorText, "releases", serviceID)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListReleasesPage(r.Context(), serviceID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Release]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeReleaseCursor("releases", serviceID, last.Sequence, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createRelease(w http.ResponseWriter, r *http.Request, serviceID string) {
	var request v1.CreateReleaseRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	request.Source.Repository = strings.TrimSpace(request.Source.Repository)
	request.Source.Ref = strings.TrimSpace(request.Source.Ref)
	request.Build.DockerfilePath = strings.TrimSpace(request.Build.DockerfilePath)
	request.Build.ComposeFile = strings.TrimSpace(request.Build.ComposeFile)
	request.Build.PrimaryService = strings.TrimSpace(request.Build.PrimaryService)
	if request.Source.Kind != v1.SourceGit || request.Source.Repository == "" || request.Source.Ref == "" {
		badPageQuery(w, "Git source kind, repository, and ref are required")
		return
	}
	if request.Intent == "" {
		request.Intent = v1.ReleaseIntentPreview
	}
	if request.Intent != v1.ReleaseIntentPreview && request.Intent != v1.ReleaseIntentProduction {
		badPageQuery(w, "intent must be preview or production")
		return
	}
	switch request.Build.Kind {
	case v1.BuildDockerfile:
		if request.Build.DockerfilePath == "" {
			badPageQuery(w, "dockerfile_path is required")
			return
		}
	case v1.BuildCompose:
		if request.Build.ComposeFile == "" || request.Build.PrimaryService == "" {
			badPageQuery(w, "compose_file and primary_service are required")
			return
		}
	default:
		badPageQuery(w, "unsupported build kind")
		return
	}
	if err := buildservice.ValidateRequest(request); err != nil {
		badPageQuery(w, "source or build specification is unsafe or unsupported")
		return
	}
	service, err := s.repository.GetService(r.Context(), serviceID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if service.State != v1.ServiceActive {
		writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition, "operation is not allowed in the current state")
		return
	}
	builder := s.releaseBuilder
	if builder == nil {
		builder = buildservice.DisabledBuilder{}
	}
	release, err := builder.BuildRelease(r.Context(), serviceID, request)
	if err != nil {
		switch {
		case errors.Is(err, buildservice.ErrUnavailable):
			idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
			if idempotencyKey == "" {
				badPageQuery(w, "idempotency_key is required to record a blocked release intent")
				return
			}
			blockedRelease, operation, recordErr := s.repository.CreateBlockedReleaseIntent(r.Context(), serviceID, request, idempotencyKey)
			if recordErr != nil {
				writeRepositoryError(w, recordErr)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{
				"release": blockedRelease, "operation": operation,
				"message": "release intent recorded; no build was started because the isolated Firecracker builder is unavailable",
			})
		case errors.Is(err, buildservice.ErrInvalidRequest):
			badPageQuery(w, "source or build specification is unsafe or unsupported")
		case errors.Is(err, buildservice.ErrBuildFailed):
			writeError(w, http.StatusBadGateway, v1.ErrorInternal,
				"isolated build failed; inspect persisted release status for the safe failure stage")
		default:
			writeRepositoryError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, release)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	release, err := s.repository.GetRelease(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, release)
}

func (s *Server) handleReleaseOperations(w http.ResponseWriter, r *http.Request, releaseID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "release operation history does not accept query parameters")
		return
	}
	if _, err := s.repository.GetRelease(r.Context(), releaseID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	items, err := s.repository.ListReleaseOperations(r.Context(), releaseID, 100)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v1.Page[v1.ReleaseOperation]{Items: items})
}

func (s *Server) handleReleasePromotion(w http.ResponseWriter, r *http.Request, releaseID string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "promotion requests do not accept query parameters")
		return
	}
	var request v1.ReleasePromotionRequest
	if err := decodeJSONBody(w, r, &request); err != nil {
		bodyError(w)
		return
	}
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if idempotencyKey == "" {
		badPageQuery(w, "idempotency_key is required for a production promotion check")
		return
	}
	operation, err := s.repository.RequestProductionPromotion(r.Context(), releaseID, idempotencyKey)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"operation": operation,
		"message":   "promotion checks recorded; production activation remains blocked until all gates and a deployment worker are available",
	})
}

func (s *Server) handleReleaseInstances(w http.ResponseWriter, r *http.Request, releaseID string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if _, err := s.repository.GetRelease(r.Context(), releaseID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "instances", releaseID)
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "instances", releaseID)
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListInstancesPage(r.Context(), releaseID, pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Instance]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("instances", releaseID, last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request, id string) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	instance, err := s.repository.GetInstance(r.Context(), id)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, instance)
}

func (s *Server) handleFleetNodes(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "fleet-nodes", "")
	if !ok {
		return
	}
	cursor, err := s.decodeTimeCursor(cursorText, "fleet-nodes", "")
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListFleetNodesPage(r.Context(), pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.FleetNode]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeTimeCursor("fleet-nodes", "", last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	pageSize, cursorText, ok := s.timePageParams(w, r, "events", "")
	if !ok {
		return
	}
	cursor, err := s.decodeEventCursor(cursorText, "events", "")
	if err != nil {
		badPageQuery(w, "invalid page cursor")
		return
	}
	items, err := s.repository.ListEventsPage(r.Context(), pageSize+1, cursor)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	page := v1.Page[v1.Event]{Items: items}
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = s.encodeEventCursor("events", "", last.OccurredAt, last.ID)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) timePageParams(w http.ResponseWriter, r *http.Request, resource, scope string) (int, string, bool) {
	pageSize, cursor, err := parsePageQuery(r)
	if err != nil {
		badPageQuery(w, "invalid pagination query")
		return 0, "", false
	}
	if cursor != "" {
		// Decode here to check that the signed cursor belongs to this resource
		// and scope before exposing it to any repository query.
		if resource == "releases" {
			if _, err := s.decodeReleaseCursor(cursor, resource, scope); err != nil {
				badPageQuery(w, "invalid page cursor")
				return 0, "", false
			}
		} else if resource == "events" {
			if _, err := s.decodeEventCursor(cursor, resource, scope); err != nil {
				badPageQuery(w, "invalid page cursor")
				return 0, "", false
			}
		} else if _, err := s.decodeTimeCursor(cursor, resource, scope); err != nil {
			badPageQuery(w, "invalid page cursor")
			return 0, "", false
		}
	}
	return pageSize, cursor, true
}

func validName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 128
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, value any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
