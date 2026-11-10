package client

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"porter/protocol"
)

func TestEnrollV2UsesFrozenPublicContract(t *testing.T) {
	const hostname = "agent-01"
	token := testJoinToken()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/fleet/enroll" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want join bearer header", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if r.Header.Get("X-Porter-Node-ID") != "" {
			t.Errorf("enrollment unexpectedly sent a node identity header")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if bytes.Contains(body, []byte(token)) {
			t.Errorf("join token appeared in request body")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Errorf("decode enrollment body: %v", err)
		} else if len(fields) != 2 || string(fields["protocol_version"]) != `"v2"` || string(fields["hostname"]) != `"agent-01"` {
			t.Errorf("enrollment JSON = %s, want only protocol_version v2 and hostname", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.AgentEnrollmentResponseV2{
			ProtocolVersion: protocol.VersionV2, NodeID: "node_issued", NodeToken: strings.Repeat("n", 43),
			EnrollmentTarget: "remote_host", ControlPlaneURL: "https://" + r.Host,
			CredentialGeneration: 1, State: "offline",
		})
	}))
	defer server.Close()

	response, err := EnrollV2(context.Background(), server.URL, token, protocol.AgentEnrollmentRequestV2{
		ProtocolVersion: protocol.VersionV2, Hostname: hostname,
	}, TLSOptions{ServerCAFile: testServerCA(t, server)})
	if err != nil {
		t.Fatalf("EnrollV2 failed: %v", err)
	}
	if response.NodeID != "node_issued" || response.CredentialGeneration != 1 || response.State != "offline" {
		t.Fatalf("unexpected enrollment response: %+v", response)
	}
}

func TestEnrollV2RequiresHTTPSEvenOnLoopback(t *testing.T) {
	_, err := EnrollV2(context.Background(), "http://127.0.0.1:8080", testJoinToken(), protocol.AgentEnrollmentRequestV2{
		ProtocolVersion: protocol.VersionV2, Hostname: "agent-01",
	}, TLSOptions{})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("EnrollV2 error = %v, want HTTPS rejection", err)
	}
}

func TestEnrollV2DoesNotFollowRedirect(t *testing.T) {
	destinationCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/capture" {
			destinationCalls++
			return
		}
		w.Header().Set("Location", "/capture")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	_, err := EnrollV2(context.Background(), server.URL, testJoinToken(), protocol.AgentEnrollmentRequestV2{
		ProtocolVersion: protocol.VersionV2, Hostname: "agent-01",
	}, TLSOptions{ServerCAFile: testServerCA(t, server)})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.HTTPStatusCode() != http.StatusFound {
		t.Fatalf("EnrollV2 error = %v, want 302 response without following redirect", err)
	}
	if destinationCalls != 0 {
		t.Fatalf("redirect target received %d requests, want none", destinationCalls)
	}
}

func TestEnrollV2MarksTransportFailureUnknownAndDoesNotRetry(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection closed after request")
	})}
	_, err := enrollV2WithClient(context.Background(), "https://control.example", testJoinToken(), protocol.AgentEnrollmentRequestV2{
		ProtocolVersion: protocol.VersionV2, Hostname: "agent-01",
	}, httpClient)
	var unknown *EnrollmentOutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("enrollment error = %v, want unknown outcome", err)
	}
	if calls != 1 {
		t.Fatalf("transport called %d times, want exactly one attempt", calls)
	}
	if strings.Contains(err.Error(), testJoinToken()) {
		t.Fatal("unknown-outcome error exposed the join token")
	}
	if !strings.Contains(err.Error(), "do not replay") || !strings.Contains(err.Error(), "exact hostname and enrollment time") || !strings.Contains(err.Error(), "exact node ID") {
		t.Fatalf("unknown-outcome guidance = %q", err.Error())
	}
}

func TestEnrollV2Classifies404AsKnownNotConsumedWithoutRetry(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/fleet/enroll" {
			t.Errorf("request = %s %s, want POST /api/v1/fleet/enroll", request.Method, request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: request}, nil
	})}
	_, err := enrollV2WithClient(context.Background(), "https://control.example", testJoinToken(), protocol.AgentEnrollmentRequestV2{
		ProtocolVersion: protocol.VersionV2, Hostname: "agent-01",
	}, httpClient)
	var notConsumed *EnrollmentOutcomeNotConsumedError
	if !errors.As(err, &notConsumed) {
		t.Fatalf("404 enrollment error = %v, want known-not-consumed", err)
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusNotFound {
		t.Fatalf("404 error chain = %v, want HTTP 404 status", err)
	}
	if calls != 1 {
		t.Fatalf("client sent %d requests, want no automatic retry", calls)
	}
}

func TestEnrollV2RequiresHandlerGuaranteeFor503RetryClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		marker   []string
		wantSafe bool
	}{
		{name: "marked pre-consumption 503", status: http.StatusServiceUnavailable, marker: []string{"not-consumed"}, wantSafe: true},
		{name: "bare 503", status: http.StatusServiceUnavailable},
		{name: "wrong marker", status: http.StatusServiceUnavailable, marker: []string{"unknown"}},
		{name: "duplicate marker", status: http.StatusServiceUnavailable, marker: []string{"not-consumed", "not-consumed"}},
		{name: "401 may indicate consumed or replayed token", status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				header := make(http.Header)
				for _, value := range tc.marker {
					header.Add(EnrollmentNotConsumedHeader, value)
				}
				return &http.Response{StatusCode: tc.status, Status: http.StatusText(tc.status), Header: header, Body: io.NopCloser(strings.NewReader("unavailable")), Request: request}, nil
			})}
			_, err := enrollV2WithClient(context.Background(), "https://control.example", testJoinToken(), protocol.AgentEnrollmentRequestV2{
				ProtocolVersion: protocol.VersionV2, Hostname: "agent-01",
			}, httpClient)
			var safe *EnrollmentOutcomeNotConsumedError
			var unknown *EnrollmentOutcomeUnknownError
			if tc.wantSafe && !errors.As(err, &safe) {
				t.Fatalf("enrollment error = %v, want known-not-consumed", err)
			}
			if !tc.wantSafe && !errors.As(err, &unknown) {
				t.Fatalf("enrollment error = %v, want unknown outcome", err)
			}
			if calls != 1 {
				t.Fatalf("client sent %d requests, want exactly one", calls)
			}
		})
	}
}

func TestJoinTokenFormat(t *testing.T) {
	if err := ValidateJoinToken(testJoinToken()); err != nil {
		t.Fatalf("valid frozen join token rejected: %v", err)
	}
	for _, invalid := range []string{"", "ptenr_short", "ptenr_" + strings.Repeat("+", 43), "ptenr_" + strings.Repeat("a", 43) + " "} {
		if err := ValidateJoinToken(invalid); err == nil {
			t.Errorf("invalid token format accepted")
		}
	}
}

func testJoinToken() string {
	return "ptenr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x91}, 32))
}

func testServerCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control-plane-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
