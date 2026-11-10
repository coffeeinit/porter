package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
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
	return &Client{Server: strings.TrimRight(server, "/"), NodeID: nodeID, NodeToken: nodeToken, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// NewHTTPClient builds a client that keeps Go's normal certificate and
// hostname verification. Optional files add a trusted CA and/or client identity.
func NewHTTPClient(options TLSOptions) (*http.Client, error) {
	if (options.ClientCertFile == "") != (options.ClientKeyFile == "") {
		return nil, fmt.Errorf("agent client certificate and key must be configured together")
	}
	if options.ServerCAFile == "" && options.ClientCertFile == "" {
		return &http.Client{Timeout: 15 * time.Second}, nil
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
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}, nil
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

func Join(ctx context.Context, server string, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	return joinWithClient(ctx, server, req, http.DefaultClient)
}

func JoinWithTLS(ctx context.Context, server string, req protocol.EnrollRequest, options TLSOptions) (protocol.EnrollResponse, error) {
	if err := validateTLSOptionsForURL(server, options); err != nil {
		return protocol.EnrollResponse{}, err
	}
	httpClient, err := NewHTTPClient(options)
	if err != nil {
		return protocol.EnrollResponse{}, err
	}
	return joinWithClient(ctx, server, req, httpClient)
}

func joinWithClient(ctx context.Context, server string, req protocol.EnrollRequest, httpClient *http.Client) (protocol.EnrollResponse, error) {
	var out protocol.EnrollResponse
	if err := validateControlPlaneURL(server); err != nil {
		return out, err
	}
	b, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/v1/fleet/enroll", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return out, fmt.Errorf("enroll: %s", resp.Status)
	}
	return out, json.NewDecoder(resp.Body).Decode(&out)
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

func (c *Client) do(ctx context.Context, method, path string, in, out interface{}) error {
	if err := validateControlPlaneURL(c.Server); err != nil {
		return err
	}
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.NodeToken)
	req.Header.Set("X-Porter-Node-ID", c.NodeID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{Method: method, Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Heartbeat(ctx context.Context, hb protocol.Heartbeat) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/heartbeat", hb, nil)
}
func (c *Client) Commands(ctx context.Context) ([]protocol.Command, error) {
	var out []protocol.Command
	err := c.do(ctx, http.MethodGet, "/v1/fleet/commands", nil, &out)
	return out, err
}
func (c *Client) Result(ctx context.Context, res protocol.CommandResult) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/result", res, nil)
}

func (c *Client) PostLogs(ctx context.Context, vmID string, lines []string) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/logs", map[string]interface{}{"vm_id": vmID, "lines": lines}, nil)
}

func (c *Client) RecordSnapshot(ctx context.Context, snap protocol.SnapshotRecord) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/snapshot", snap, nil)
}

func (c *Client) ReportVMState(ctx context.Context, vmID, state, health, errText string) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/vmstate", map[string]string{"vm_id": vmID, "state": state, "health": health, "error": errText}, nil)
}
