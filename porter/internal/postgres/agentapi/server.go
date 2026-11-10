// Package agentapi implements the TLS-only v2 Fleet agent transport and
// PostgreSQL-backed enrollment and credential handlers. The PostgreSQL-only
// server mounts it after verifying the required migration schema.
package agentapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
	"porter/protocol"
)

const (
	DefaultLeaseDuration = 5 * time.Minute
	DefaultRotationGrace = 5 * time.Minute
	MaxRequestBodyBytes  = 64 << 10
)

type Repository interface {
	CreateAgentEnrollmentToken(ctx context.Context, target v1.AgentEnrollmentTarget, controlPlaneURL string, now time.Time) (v1.AgentEnrollmentTokenIssue, error)
	RedeemAgentEnrollmentToken(ctx context.Context, token string, request protocol.AgentEnrollmentRequestV2, now time.Time) (protocol.AgentEnrollmentResponseV2, error)
	ProvisionNodeCredential(ctx context.Context, nodeID string, now time.Time, rotationGrace time.Duration) (pgstore.NodeCredentialIssue, error)
	AuthenticateNodeCredential(ctx context.Context, nodeID, token string, now time.Time) (bool, error)
	RecordAgentHeartbeat(ctx context.Context, nodeID string, now time.Time) error
	IngestAgentTelemetry(ctx context.Context, authenticatedNodeID string, batch protocol.TelemetryBatchV2, now time.Time) (pgstore.TelemetryIngestCounts, error)
	RequeueExpiredCommandsForNode(ctx context.Context, nodeID string, now time.Time, limit int) (int, error)
	LeaseProtocolCommandsAt(ctx context.Context, nodeID string, limit int, duration time.Duration, now time.Time) ([]protocol.CommandV2, error)
	RecordProtocolCommandResult(ctx context.Context, result protocol.CommandResultV2) error
}

type Config struct {
	LeaseDuration time.Duration
	RotationGrace time.Duration
	Now           func() time.Time
}

type Server struct {
	repository    Repository
	adminToken    string
	adminTokenSum [32]byte
	leaseDuration time.Duration
	rotationGrace time.Duration
	now           func() time.Time
}

func New(repository Repository, adminToken string) (*Server, error) {
	return NewWithConfig(repository, adminToken, Config{})
}

func NewWithConfig(repository Repository, adminToken string, config Config) (*Server, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL agent repository is required")
	}
	if strings.TrimSpace(adminToken) == "" {
		return nil, errors.New("operator admin token is required")
	}
	if config.LeaseDuration == 0 {
		config.LeaseDuration = DefaultLeaseDuration
	}
	if config.RotationGrace == 0 {
		config.RotationGrace = DefaultRotationGrace
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.LeaseDuration < time.Second || config.LeaseDuration > 15*time.Minute {
		return nil, errors.New("agent command lease duration must be between 1 second and 15 minutes")
	}
	if config.RotationGrace < 0 || config.RotationGrace > 30*time.Minute {
		return nil, errors.New("agent credential rotation grace must not exceed 30 minutes")
	}
	return &Server{
		repository: repository, adminToken: adminToken,
		adminTokenSum: sha256.Sum256([]byte(adminToken)),
		leaseDuration: config.LeaseDuration, rotationGrace: config.RotationGrace, now: config.Now,
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		writeError(w, http.StatusUpgradeRequired, v1.ErrorFailedPrecondition, "v2 agent transport requires HTTPS")
		return
	}
	switch {
	case r.URL.Path == "/internal/agent/enroll":
		s.handleEnrollment(w, r)
	case r.URL.Path == "/internal/agent/heartbeat":
		s.handleHeartbeat(w, r)
	case r.URL.Path == "/internal/agent/telemetry":
		s.handleTelemetry(w, r)
	case r.URL.Path == "/internal/agent/commands":
		s.handleCommands(w, r)
	case r.URL.Path == "/internal/agent/results":
		s.handleResult(w, r)
	case r.URL.Path == "/internal/admin/fleet/enrollment-tokens":
		s.handleEnrollmentTokenIssue(w, r)
	case strings.HasPrefix(r.URL.Path, "/internal/admin/fleet/nodes/"):
		s.handleCredentialProvisioning(w, r)
	default:
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "route not found")
	}
}

// FleetCapabilitiesHandler returns an operator-authenticated probe for the
// public mux's actual route registrations. The caller supplies flags computed
// from handlers that were successfully mounted.
func (s *Server) FleetCapabilitiesHandler(capabilities v1.FleetCapabilities) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			writeError(w, http.StatusUpgradeRequired, v1.ErrorFailedPrecondition, "fleet capability probe requires HTTPS")
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if r.URL.RawQuery != "" {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "fleet capability probe does not accept query parameters")
			return
		}
		if !s.authorizedAdmin(r.Header.Get("Authorization")) {
			writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "operator bearer token required")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		writeJSON(w, http.StatusOK, capabilities)
	})
}

func (s *Server) handleEnrollmentTokenIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "enrollment-token issue does not accept query parameters")
		return
	}
	if !s.authorizedAdmin(r.Header.Get("Authorization")) {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "operator bearer token required")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "Content-Type must be application/json")
		return
	}
	var request v1.CreateAgentEnrollmentTokenRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid enrollment-token request")
		return
	}
	if !validEnrollmentIssueRequest(request) {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "target and HTTPS control-plane origin are required")
		return
	}
	issue, err := s.repository.CreateAgentEnrollmentToken(r.Context(), request.Target, request.ControlPlaneURL, s.now().UTC())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, issue)
}

func (s *Server) handleEnrollment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "agent enrollment does not accept query parameters")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "valid unexpired enrollment token required")
		return
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "Content-Type must be application/json")
		return
	}
	var request protocol.AgentEnrollmentRequestV2
	if err := decodeStrictJSON(w, r, &request); err != nil || request.Validate() != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid v2 agent enrollment request")
		return
	}
	response, err := s.repository.RedeemAgentEnrollmentToken(r.Context(), token, request, s.now().UTC())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, response)
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func validEnrollmentIssueRequest(request v1.CreateAgentEnrollmentTokenRequest) bool {
	if request.Target != v1.AgentEnrollmentSameHost && request.Target != v1.AgentEnrollmentRemoteHost {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(request.ControlPlaneURL))
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == "" && (parsed.Path == "" || parsed.Path == "/")
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "heartbeat does not accept query parameters")
		return
	}
	nodeID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	var heartbeat protocol.AgentHeartbeatV2
	if err := decodeStrictJSON(w, r, &heartbeat); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid v2 agent heartbeat")
		return
	}
	now := s.now().UTC()
	if err := heartbeat.ValidateAt(now); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid or stale v2 agent heartbeat")
		return
	}
	if err := s.repository.RecordAgentHeartbeat(r.Context(), nodeID, now); err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if !onlyQueryKeys(r, "limit") {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "commands accepts only the optional limit query parameter")
		return
	}
	if values, exists := r.URL.Query()["limit"]; exists && len(values) != 1 {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "limit may be specified only once")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	nodeID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	now := s.now().UTC()
	if _, err := s.repository.RequeueExpiredCommandsForNode(r.Context(), nodeID, now, 100); err != nil {
		writeRepositoryError(w, err)
		return
	}
	commands, err := s.repository.LeaseProtocolCommandsAt(r.Context(), nodeID, limit, s.leaseDuration, now)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	for _, command := range commands {
		if err := command.ValidateAt(now); err != nil || !agentCommandKindAllowed(command.Kind) {
			writeError(w, http.StatusInternalServerError, v1.ErrorInternal, "repository returned an invalid or unsupported agent command")
			return
		}
	}
	if commands == nil {
		commands = []protocol.CommandV2{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, commands)
}

func agentCommandKindAllowed(kind string) bool {
	switch kind {
	case "vm.apply", "vm.delete", "vm.start", "vm.stop", "vm.restart", "snapshot.create", "snapshot.restore":
		return true
	default:
		return false
	}
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "result submission does not accept query parameters")
		return
	}
	nodeID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	var result protocol.CommandResultV2
	if err := decodeStrictJSON(w, r, &result); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid v2 command result")
		return
	}
	if err := result.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid v2 command result")
		return
	}
	if result.NodeID != nodeID {
		writeError(w, http.StatusForbidden, v1.ErrorPermissionDenied, "result node does not match the authenticated node")
		return
	}
	if err := s.repository.RecordProtocolCommandResult(r.Context(), result); err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCredentialProvisioning(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "credential provisioning accepts no query parameters")
		return
	}
	if r.ContentLength != 0 {
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "Content-Type must be application/json")
			return
		}
		var emptyRequest struct{}
		if err := decodeStrictJSON(w, r, &emptyRequest); err != nil {
			writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "credential provisioning accepts no request fields")
			return
		}
	}
	const prefix = "/internal/admin/fleet/nodes/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] != "credentials" || !validNodeID(parts[0]) {
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "route not found")
		return
	}
	if !s.authorizedAdmin(r.Header.Get("Authorization")) {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "operator bearer token required")
		return
	}
	issue, err := s.repository.ProvisionNodeCredential(r.Context(), parts[0], s.now().UTC(), s.rotationGrace)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, issue)
}

func (s *Server) authenticateAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	nodeID := r.Header.Get("X-Porter-Node-ID")
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !validNodeID(nodeID) {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "valid provisioned node credentials are required")
		return "", false
	}
	valid, err := s.repository.AuthenticateNodeCredential(r.Context(), nodeID, token, s.now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, v1.ErrorInternal, "authenticate node credentials")
		return "", false
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "valid provisioned node credentials are required")
		return "", false
	}
	return nodeID, true
}

func (s *Server) authorizedAdmin(header string) bool {
	token, ok := bearerToken(header)
	if !ok {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], s.adminTokenSum[:]) == 1
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || scheme != "Bearer" || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func validNodeID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n\t ")
}

func onlyQueryKeys(r *http.Request, allowed ...string) bool {
	query := r.URL.Query()
	for key := range query {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, v1.ErrorBadRequest, "method not allowed")
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgstore.ErrEnrollmentTokenUnavailable):
		writeError(w, http.StatusUnauthorized, v1.ErrorUnauthenticated, "valid unexpired enrollment token required")
	case errors.Is(err, pgstore.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, v1.ErrorBadRequest, "invalid request")
	case errors.Is(err, pgstore.ErrNotFound):
		writeError(w, http.StatusNotFound, v1.ErrorNotFound, "resource not found")
	case errors.Is(err, pgstore.ErrTelemetryConflict):
		writeError(w, http.StatusConflict, v1.ErrorConflict, "telemetry record conflicts with a previously accepted record")
	case errors.Is(err, pgstore.ErrFailedPrecondition):
		writeError(w, http.StatusPreconditionFailed, v1.ErrorFailedPrecondition, "operation is not allowed in the current state")
	case errors.Is(err, pgstore.ErrCommandAttemptMismatch),
		errors.Is(err, pgstore.ErrCommandLeaseMismatch),
		errors.Is(err, pgstore.ErrCommandLeaseExpired):
		w.Header().Set("X-Porter-Result-Stale", "true")
		writeError(w, http.StatusConflict, v1.ErrorConflict, "command result lease is stale")
	case errors.Is(err, pgstore.ErrCommandNodeMismatch),
		errors.Is(err, pgstore.ErrCommandOperationMismatch),
		errors.Is(err, pgstore.ErrCommandKindMismatch),
		errors.Is(err, pgstore.ErrCommandStateConflict),
		errors.Is(err, pgstore.ErrCommandResultConflict):
		writeError(w, http.StatusConflict, v1.ErrorConflict, "command result conflicts with the active lease")
	default:
		writeError(w, http.StatusInternalServerError, v1.ErrorInternal, "agent API persistence failed")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code v1.ErrorCode, message string) {
	var raw [8]byte
	requestID := "req_unavailable"
	if _, err := rand.Read(raw[:]); err == nil {
		requestID = "req_" + hex.EncodeToString(raw[:])
	}
	w.Header().Set("X-Request-ID", requestID)
	writeJSON(w, status, v1.ErrorResponse{Error: v1.ErrorDetail{Code: code, Message: message, RequestID: requestID}})
}
