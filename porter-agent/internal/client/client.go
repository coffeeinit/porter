package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"porter/protocol"
)

// HTTPStatusError reports an HTTP response that the server did not accept.
type HTTPStatusError struct {
	Method string
	Path   string
	Code   int
	Status string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Status)
}

func (e *HTTPStatusError) HTTPStatusCode() int { return e.Code }

type EnrollmentOutcomeUnknownError struct{ cause error }

func (e *EnrollmentOutcomeUnknownError) Error() string {
	return "enrollment outcome is unknown; do not replay this one-time join token. An operator must find the Fleet node by the exact hostname and enrollment time, verify its exact node ID, then rotate credentials through admin Fleet; stop if the match is missing or ambiguous"
}
func (e *EnrollmentOutcomeUnknownError) Unwrap() error { return e.cause }

// EnrollmentOutcomeNotConsumedError is returned only for an explicit 404 or a
// 503 whose handler response guarantees the one-time token was not consumed.
// The client never retries; a later user-invoked join may use the same token.
type EnrollmentOutcomeNotConsumedError struct{ cause error }

func (e *EnrollmentOutcomeNotConsumedError) Error() string {
	return "enrollment handler confirmed the join token was not consumed; no automatic retry was made. A later user-invoked join may retry with the same token"
}
func (e *EnrollmentOutcomeNotConsumedError) Unwrap() error { return e.cause }

// EnrollmentNotConsumedHeader is the explicit 503 safety signal. A handler may
// set it only when it can guarantee that this request did not consume the token.
const EnrollmentNotConsumedHeader = "X-Porter-Enrollment-Outcome"

const enrollmentNotConsumedValue = "not-consumed"

func NewEnrollmentOutcomeUnknownError(cause error) *EnrollmentOutcomeUnknownError {
	return &EnrollmentOutcomeUnknownError{cause: cause}
}

var ErrLegacyV1EnrollmentDisabled = errors.New("legacy v1 agent enrollment is disabled; use the PostgreSQL-backed v2 join flow")

type TLSOptions struct {
	ServerCAFile   string
	ClientCertFile string
	ClientKeyFile  string
}

type Client struct {
	Server    string
	NodeID    string
	NodeToken string
	HTTP      *http.Client
}

func New(server, nodeID, nodeToken string) *Client {
	return &Client{Server: strings.TrimRight(server, "/"), NodeID: nodeID, NodeToken: nodeToken, HTTP: noRedirectClient(nil)}
}

func noRedirectClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewHTTPClient builds a client that keeps Go's normal certificate and
// hostname verification. Optional files add a trusted CA and/or client identity.
func NewHTTPClient(options TLSOptions) (*http.Client, error) {
	if (options.ClientCertFile == "") != (options.ClientKeyFile == "") {
		return nil, fmt.Errorf("agent client certificate and key must be configured together")
	}
	if options.ServerCAFile == "" && options.ClientCertFile == "" {
		return noRedirectClient(nil), nil
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport is not an *http.Transport")
	}
	transport := defaultTransport.Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if options.ServerCAFile != "" {
		caPEM, err := os.ReadFile(options.ServerCAFile)
		if err != nil {
			return nil, fmt.Errorf("read control-plane CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("control-plane CA file contains no valid certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if options.ClientCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(options.ClientCertFile, options.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load agent client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport.TLSClientConfig = tlsConfig
	return noRedirectClient(transport), nil
}

func NewWithTLS(server, nodeID, nodeToken string, options TLSOptions) (*Client, error) {
	if err := validateTLSOptionsForURL(server, options); err != nil {
		return nil, err
	}
	httpClient, err := NewHTTPClient(options)
	if err != nil {
		return nil, err
	}
	return &Client{Server: strings.TrimRight(server, "/"), NodeID: nodeID, NodeToken: nodeToken, HTTP: httpClient}, nil
}

// Join and JoinWithTLS fail closed for callers compiled against the former v1
// API. They never send the old token-in-JSON request.
func Join(context.Context, string, protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	return protocol.EnrollResponse{}, ErrLegacyV1EnrollmentDisabled
}

func JoinWithTLS(context.Context, string, protocol.EnrollRequest, TLSOptions) (protocol.EnrollResponse, error) {
	return protocol.EnrollResponse{}, ErrLegacyV1EnrollmentDisabled
}

// ValidateEnrollmentURL is stricter than the runtime URL check: enrollment
// always requires HTTPS, including for localhost.
func ValidateEnrollmentURL(raw string) error {
	if err := validateControlPlaneURL(raw); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("agent enrollment requires HTTPS")
	}
	return nil
}

// ValidateJoinToken checks the frozen ptenr_ + 32-byte raw-base64url form.
func ValidateJoinToken(token string) error {
	const prefix = "ptenr_"
	if !strings.HasPrefix(token, prefix) || strings.ContainsAny(token, "\r\n\t ") {
		return fmt.Errorf("join token has an invalid format")
	}
	encoded := strings.TrimPrefix(token, prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return fmt.Errorf("join token has an invalid format")
	}
	return nil
}

// EnrollV2 makes exactly one POST to the public PostgreSQL-backed route. Only
// 404 or a handler-marked pre-consumption 503 is safe for a later manual retry;
// all other unsuccessful or undecodable outcomes are unknown. This never retries.
func EnrollV2(ctx context.Context, server, joinToken string, request protocol.AgentEnrollmentRequestV2, options TLSOptions) (protocol.AgentEnrollmentResponseV2, error) {
	if err := ValidateEnrollmentURL(server); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	if err := ValidateJoinToken(joinToken); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	if err := request.Validate(); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	httpClient, err := NewHTTPClient(options)
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	return enrollV2WithClient(ctx, server, joinToken, request, httpClient)
}

func enrollV2WithClient(ctx context.Context, server, joinToken string, request protocol.AgentEnrollmentRequestV2, httpClient *http.Client) (protocol.AgentEnrollmentResponseV2, error) {
	var out protocol.AgentEnrollmentResponseV2
	body, err := json.Marshal(request)
	if err != nil {
		return out, fmt.Errorf("encode enrollment request: %w", err)
	}
	endpoint := strings.TrimRight(server, "/") + "/api/v1/fleet/enroll"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("create enrollment request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+joinToken)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return out, &EnrollmentOutcomeUnknownError{cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		statusErr := &HTTPStatusError{Method: http.MethodPost, Path: "/api/v1/fleet/enroll", Code: resp.StatusCode, Status: resp.Status}
		if resp.StatusCode == http.StatusNotFound || (resp.StatusCode == http.StatusServiceUnavailable && len(resp.Header.Values(EnrollmentNotConsumedHeader)) == 1 && resp.Header.Get(EnrollmentNotConsumedHeader) == enrollmentNotConsumedValue) {
			return out, &EnrollmentOutcomeNotConsumedError{cause: statusErr}
		}
		return out, NewEnrollmentOutcomeUnknownError(statusErr)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&out); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, &EnrollmentOutcomeUnknownError{cause: err}
	}
	if err := out.Validate(); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, &EnrollmentOutcomeUnknownError{cause: err}
	}
	if !validBearerToken(out.NodeToken) || len(out.NodeToken) > 512 {
		return protocol.AgentEnrollmentResponseV2{}, &EnrollmentOutcomeUnknownError{cause: fmt.Errorf("enrollment response contains an invalid node credential")}
	}
	if err := ValidateEnrollmentURL(out.ControlPlaneURL); err != nil || !sameOrigin(server, out.ControlPlaneURL) {
		return protocol.AgentEnrollmentResponseV2{}, &EnrollmentOutcomeUnknownError{cause: fmt.Errorf("enrollment response control-plane URL does not match the requested server")}
	}
	return out, nil
}

func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.User != nil || ub.User != nil {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(strings.TrimSuffix(ua.Hostname(), "."), strings.TrimSuffix(ub.Hostname(), ".")) && port(ua) == port(ub)
}

func validBearerToken(token string) bool {
	if token == "" {
		return false
	}
	for _, character := range []byte(token) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validateControlPlaneURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid control-plane URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("remote control-plane URL must use HTTPS; plaintext HTTP is allowed only for localhost development")
	default:
		return fmt.Errorf("control-plane URL must use HTTPS")
	}
}

func validateTLSOptionsForURL(raw string, options TLSOptions) error {
	if err := validateControlPlaneURL(raw); err != nil {
		return err
	}
	if options.ServerCAFile == "" && options.ClientCertFile == "" && options.ClientKeyFile == "" {
		return nil
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("agent TLS options require an HTTPS control-plane URL")
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out interface{}, wantStatus int) error {
	if err := ValidateEnrollmentURL(c.Server); err != nil {
		return fmt.Errorf("agent control-plane transport requires HTTPS: %w", err)
	}
	if c.NodeID == "" || !validBearerToken(c.NodeToken) {
		return fmt.Errorf("agent node identity or bearer credential is invalid")
	}
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode agent request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.NodeToken)
	req.Header.Set("X-Porter-Node-ID", c.NodeID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return &HTTPStatusError{Method: method, Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	if out != nil {
		decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
		if err := decoder.Decode(out); err != nil {
			return fmt.Errorf("decode agent response: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return fmt.Errorf("agent response contains trailing data")
		}
	}
	return nil
}

// NewTelemetryRecordID returns a safe random identifier for one telemetry item.
func NewTelemetryRecordID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate telemetry record ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (c *Client) Heartbeat(ctx context.Context, hb protocol.AgentHeartbeatV2) error {
	if err := hb.ValidateAt(time.Now().UTC()); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/internal/agent/heartbeat", hb, nil, http.StatusNoContent)
}

func (c *Client) Commands(ctx context.Context) ([]protocol.CommandV2, error) {
	var out []protocol.CommandV2
	err := c.do(ctx, http.MethodGet, "/internal/agent/commands?limit=20", nil, &out, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if len(out) > 20 {
		return nil, fmt.Errorf("control plane returned more commands than the requested limit")
	}
	for _, command := range out {
		if err := command.Validate(); err != nil {
			return nil, fmt.Errorf("control plane returned an invalid command: %w", err)
		}
		if command.NodeID != c.NodeID {
			return nil, fmt.Errorf("control plane returned a command for another node")
		}
	}
	return out, nil
}

func (c *Client) Result(ctx context.Context, res protocol.CommandResultV2) error {
	if err := res.Validate(); err != nil {
		return err
	}
	if res.NodeID != c.NodeID {
		return fmt.Errorf("command result node does not match the authenticated node")
	}
	return c.do(ctx, http.MethodPost, "/internal/agent/results", res, nil, http.StatusNoContent)
}

func (c *Client) Telemetry(ctx context.Context, batch protocol.TelemetryBatchV2) error {
	if batch.NodeID != c.NodeID {
		return fmt.Errorf("telemetry node does not match the authenticated node")
	}
	if err := batch.Validate(); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/internal/agent/telemetry", batch, nil, http.StatusNoContent)
}

// PostLogs sends runtime log lines only through v2 telemetry batches.
func (c *Client) PostLogs(ctx context.Context, vmID string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > 256 {
		lines = lines[len(lines)-256:]
	}
	now := time.Now().UTC()
	batch := protocol.TelemetryBatchV2{ProtocolVersion: protocol.VersionV2, NodeID: c.NodeID, Logs: make([]protocol.TelemetryLogV2, 0, len(lines))}
	for _, line := range lines {
		if len(line) > 4096 {
			line = line[:4096]
		}
		recordID, err := NewTelemetryRecordID()
		if err != nil {
			return err
		}
		batch.Logs = append(batch.Logs, protocol.TelemetryLogV2{RecordID: recordID, InstanceID: vmID, ReportedAt: now, Level: "info", Message: line})
	}
	return c.Telemetry(ctx, batch)
}
