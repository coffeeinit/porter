// Package httpapi implements the v1 control-plane API as a separate adapter.
// The active server may mount it only when PostgreSQL is explicitly configured.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/buildservice"
	pgstore "porter/controlplane/internal/postgres/store"
)

type Repository interface {
	CreateProject(ctx context.Context, name string) (v1.Project, error)
	GetProject(ctx context.Context, id string) (v1.Project, error)
	ListProjectsPage(ctx context.Context, limit int, after *pgstore.TimeCursor) ([]v1.Project, error)
	CreateService(ctx context.Context, projectID string, request v1.CreateServiceRequest) (v1.Service, error)
	GetService(ctx context.Context, id string) (v1.Service, error)
	ListServicesPage(ctx context.Context, projectID string, limit int, after *pgstore.TimeCursor) ([]v1.Service, error)
	GetRelease(ctx context.Context, id string) (v1.Release, error)
	ListReleasesPage(ctx context.Context, serviceID string, limit int, after *pgstore.ReleaseCursor) ([]v1.Release, error)
	CreateBlockedReleaseIntent(ctx context.Context, serviceID string, request v1.CreateReleaseRequest, idempotencyKey string) (v1.Release, v1.ReleaseOperation, error)
	ListReleaseOperations(ctx context.Context, releaseID string, limit int) ([]v1.ReleaseOperation, error)
	RequestProductionPromotion(ctx context.Context, releaseID, idempotencyKey string) (v1.ReleaseOperation, error)
	GetInstance(ctx context.Context, id string) (v1.Instance, error)
	ListInstancesPage(ctx context.Context, releaseID string, limit int, after *pgstore.TimeCursor) ([]v1.Instance, error)
	ListFleetNodesPage(ctx context.Context, limit int, after *pgstore.TimeCursor) ([]v1.FleetNode, error)
	ListEventsPage(ctx context.Context, limit int, after *pgstore.EventCursor) ([]v1.Event, error)
	ListTelemetryLogsPage(ctx context.Context, limit int, filter pgstore.TelemetryFilter, after *pgstore.EventCursor) ([]v1.TelemetryLog, error)
	ListTelemetryMetricsPage(ctx context.Context, limit int, filter pgstore.TelemetryFilter, after *pgstore.EventCursor) ([]v1.TelemetryMetric, error)
	GetOperation(ctx context.Context, id string) (v1.Operation, error)
	ListOperationsPage(ctx context.Context, limit int, after *pgstore.TimeCursor) ([]v1.Operation, error)
	CreateDomainClaim(ctx context.Context, projectID, serviceID, hostname string) (v1.DomainClaim, error)
	GetDomainClaim(ctx context.Context, id string) (v1.DomainClaim, error)
	ListDomainClaimsPage(ctx context.Context, projectID, serviceID string, limit int, after *pgstore.TimeCursor) ([]v1.DomainClaim, error)
	RecordDomainTXTProof(ctx context.Context, id, observedValue string) (v1.DomainClaim, error)
	CreateGatewayRoute(ctx context.Context, claimID, pathPrefix string) (v1.GatewayRoute, error)
	GetGatewayRoute(ctx context.Context, id string) (v1.GatewayRoute, error)
	ListGatewayRoutesPage(ctx context.Context, claimID string, limit int, after *pgstore.TimeCursor) ([]v1.GatewayRoute, error)
	SetGatewayRouteDesiredEnabled(ctx context.Context, id string, desiredEnabled bool) (v1.GatewayRoute, error)
	CreateVolume(ctx context.Context, projectID, serviceID string, request v1.CreateVolumeRequest) (v1.Volume, error)
	GetVolume(ctx context.Context, id string) (v1.Volume, error)
	ListVolumesPage(ctx context.Context, projectID, serviceID string, limit int, after *pgstore.TimeCursor) ([]v1.Volume, error)
	ListProjectInstancesPage(ctx context.Context, projectID string, limit int, after *pgstore.TimeCursor) ([]v1.Instance, error)
	QueueSnapshotCreateOperation(ctx context.Context, instanceID, consistency, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error)
	QueueSnapshotRestoreOperation(ctx context.Context, instanceID, snapshotID, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error)
	CreateSnapshotMetadata(ctx context.Context, instanceID, consistency string) (v1.SnapshotMetadata, error)
	GetSnapshotMetadata(ctx context.Context, id string) (v1.SnapshotMetadata, error)
	ListSnapshotMetadataPage(ctx context.Context, projectID, instanceID string, limit int, after *pgstore.TimeCursor) ([]v1.SnapshotMetadata, error)
	CreateBackupMetadata(ctx context.Context, snapshotID, consistency string) (v1.BackupMetadata, error)
	GetBackupMetadata(ctx context.Context, id string) (v1.BackupMetadata, error)
	ListBackupMetadataPage(ctx context.Context, projectID, snapshotID string, limit int, after *pgstore.TimeCursor) ([]v1.BackupMetadata, error)
}

type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type Server struct {
	repository     Repository
	adminToken     string
	cursorKey      []byte
	txtResolver    TXTResolver
	releaseBuilder buildservice.ReleaseBuilder
}

func New(repository Repository, adminToken string) (*Server, error) {
	return NewWithTXTResolver(repository, adminToken, net.DefaultResolver)
}

func NewWithTXTResolver(repository Repository, adminToken string, resolver TXTResolver) (*Server, error) {
	return NewWithReleaseBuilder(repository, adminToken, resolver, buildservice.DisabledBuilder{})
}

// NewWithReleaseBuilder is an explicit injection point for a fully verified
// isolated builder. Normal server construction always uses DisabledBuilder.
func NewWithReleaseBuilder(repository Repository, adminToken string, resolver TXTResolver, builder buildservice.ReleaseBuilder) (*Server, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL resource repository is required")
	}
	if adminToken == "" {
		return nil, errors.New("operator admin token is required")
	}
	if resolver == nil {
		return nil, errors.New("DNS TXT resolver is required")
	}
	if builder == nil {
		builder = buildservice.DisabledBuilder{}
	}
	key := sha256.Sum256([]byte("porter-control-plane-v1-cursor\x00" + adminToken))
	return &Server{repository: repository, adminToken: adminToken, cursorKey: key[:], txtResolver: resolver, releaseBuilder: builder}, nil
}

// UnavailableHandler fails closed when the PostgreSQL API is unavailable.
// It never delegates requests to the legacy SQLite API.
func UnavailableHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusServiceUnavailable, v1.ErrorFailedPrecondition,
			"PostgreSQL API is unavailable; configure systemd credentials and apply migrations separately")
	})
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r.Header.Get("Authorization")) {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "admin bearer token required")
		return
	}
	s.route(w, r)
}

func (s *Server) authorized(header string) bool {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || scheme != "Bearer" || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(s.adminToken))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (s *Server) handleUnavailableOperationAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if r.URL.RawQuery != "" {
		badPageQuery(w, "operation actions do not accept query parameters")
		return
	}
	if _, err := s.repository.GetOperation(r.Context(), id); err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition,
		"operation "+action+" is unavailable until agent authentication, protocol compatibility, and durable worker handling are implemented")
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "route not found")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" {
			writeError(w, http.StatusNotFound, v1.ErrorNotFound, "route not found")
			return
		}
	}

	switch {
	case len(segments) == 1 && segments[0] == "domain-claims":
		s.handleDomainClaims(w, r)
	case len(segments) == 2 && segments[0] == "domain-claims":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleDomainClaim(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "domain-claims" && segments[2] == "verify-txt":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.verifyDomainClaimTXT(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "domain-claims" && segments[2] == "routes":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleDomainClaimRoutes(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "gateway-routes":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleGatewayRoute(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "volumes":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleVolume(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "snapshots":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleSnapshotMetadata(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "snapshots" && segments[2] == "backup-records":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleSnapshotBackups(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "backups":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleBackupMetadata(w, r, segments[1])
	case len(segments) == 5 && segments[0] == "projects" && segments[2] == "services" && segments[4] == "domain-claims":
		if !validID(segments[1]) || !validID(segments[3]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectServiceDomainClaims(w, r, segments[1], segments[3])
	case len(segments) == 1 && segments[0] == "projects":
		s.handleProjects(w, r)
	case len(segments) == 2 && segments[0] == "projects":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProject(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "projects" && segments[2] == "services":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectServices(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "projects" && segments[2] == "instances":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectInstances(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "projects" && segments[2] == "volumes":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectVolumes(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "projects" && segments[2] == "snapshots":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectSnapshots(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "projects" && segments[2] == "backups":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectBackups(w, r, segments[1])
	case len(segments) == 5 && segments[0] == "projects" && segments[2] == "services" && segments[4] == "volumes":
		if !validID(segments[1]) || !validID(segments[3]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleProjectServiceVolumes(w, r, segments[1], segments[3])
	case len(segments) == 2 && segments[0] == "services":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleService(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "services" && segments[2] == "releases":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleServiceReleases(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "releases":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleRelease(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "releases" && segments[2] == "operations":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleReleaseOperations(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "releases" && segments[2] == "promote":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleReleasePromotion(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "releases" && segments[2] == "instances":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleReleaseInstances(w, r, segments[1])
	case len(segments) == 2 && segments[0] == "instances":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleInstance(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "instances" && segments[2] == "snapshots":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleInstanceSnapshots(w, r, segments[1])
	case len(segments) == 4 && segments[0] == "instances" && segments[2] == "snapshots" && segments[3] == "capture":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleSnapshotCapture(w, r, segments[1])
	case len(segments) == 5 && segments[0] == "instances" && segments[2] == "snapshots" && segments[4] == "restore":
		if !validID(segments[1]) || !validID(segments[3]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleSnapshotRestore(w, r, segments[1], segments[3])
	case len(segments) == 2 && segments[0] == "fleet" && segments[1] == "nodes":
		s.handleFleetNodes(w, r)
	case len(segments) == 1 && segments[0] == "operations":
		s.handleOperations(w, r)
	case len(segments) == 3 && segments[0] == "operations" && (segments[2] == "retry" || segments[2] == "cancel" || segments[2] == "dispatch"):
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleUnavailableOperationAction(w, r, segments[1], segments[2])
	case len(segments) == 2 && segments[0] == "operations":
		if !validID(segments[1]) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid resource ID")
			return
		}
		s.handleOperation(w, r, segments[1])
	case len(segments) == 1 && segments[0] == "events":
		s.handleEvents(w, r)
	case len(segments) == 1 && segments[0] == "logs":
		s.handleTelemetryLogs(w, r)
	case len(segments) == 1 && segments[0] == "metrics":
		s.handleTelemetryMetrics(w, r)
	default:
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "route not found")
	}
}

func validID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\")
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(w, http.StatusMethodNotAllowed, v1.ErrorBadRequest, "method not allowed")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code v1.ErrorCode, message string) {
	requestID := newRequestID()
	w.Header().Set("X-Request-ID", requestID)
	writeJSON(w, status, v1.ErrorResponse{Error: v1.ErrorDetail{
		Code: code, Message: message, RequestID: requestID,
	}})
}

func newRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "req_unavailable"
	}
	return "req_" + hex.EncodeToString(raw[:])
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgstore.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid request")
	case errors.Is(err, pgstore.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, v1.ErrorConflict, "Idempotency-Key was already used for a different request")
	case errors.Is(err, pgstore.ErrNotFound):
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "resource not found")
	case errors.Is(err, pgstore.ErrFailedPrecondition):
		writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition, "operation is not allowed in the current state")
	default:
		var stateError interface{ SQLState() string }
		if errors.As(err, &stateError) {
			switch stateError.SQLState() {
			case "23505":
				writeError(w, http.StatusConflict, v1.ErrorConflict, "resource conflicts with an existing record")
				return
			case "23503", "23514":
				writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition, "operation conflicts with a resource constraint")
				return
			}
		}
		writeError(w, http.StatusInternalServerError, v1.ErrorInternal, "request could not be completed")
	}
}

func bodyError(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid JSON request body")
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	methodNotAllowed(w, method)
	return false
}

func badPageQuery(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, message)
}
