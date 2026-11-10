package client

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"porter/protocol"
)

func TestValidateControlPlaneURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{name: "https remote", url: "https://control.example.com:8443", want: true},
		{name: "http localhost", url: "http://localhost:8080", want: true},
		{name: "http ipv4 loopback", url: "http://127.0.0.1:8080", want: true},
		{name: "http ipv6 loopback", url: "http://[::1]:8080", want: true},
		{name: "remote http", url: "http://control.example.com:8080"},
		{name: "non-http scheme", url: "ws://control.example.com"},
		{name: "userinfo", url: "https://user:pass@control.example.com"},
		{name: "query", url: "https://control.example.com/path?secret=x"},
		{name: "relative", url: "/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateControlPlaneURL(tc.url)
			if tc.want && err != nil {
				t.Fatalf("validate %q: %v", tc.url, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("validate %q succeeded, want rejection", tc.url)
			}
		})
	}
}

func TestLegacyJoinIsDisabledWithoutSendingToken(t *testing.T) {
	const secret = "legacy-join-secret"
	_, err := Join(context.Background(), "http://control.example.com:8080", protocol.EnrollRequest{Token: secret})
	if !errors.Is(err, ErrLegacyV1EnrollmentDisabled) {
		t.Fatalf("Join error = %v, want legacy enrollment disabled", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("legacy join error included the enrollment secret")
	}
}

func TestClientRejectsRemotePlainHTTPBeforeSendingNodeToken(t *testing.T) {
	cl := New("http://control.example.com:8080", "node-1", "node-secret")
	err := cl.Heartbeat(context.Background(), protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Heartbeat error = %v, want HTTPS rejection", err)
	}
}

func TestResultReturnsTypedHTTPStatusError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	cl, err := NewWithTLS(server.URL, "node-1", "node-token", TLSOptions{ServerCAFile: testServerCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	leaseToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	err = cl.Result(context.Background(), protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-1", Attempt: 1,
		Kind: "vm.start", LeaseToken: leaseToken, Success: true, State: "running",
	})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Result error = %v, want *HTTPStatusError", err)
	}
	if statusErr.HTTPStatusCode() != http.StatusConflict || statusErr.Path != "/internal/agent/results" {
		t.Fatalf("HTTP status error = %+v, want conflict on v2 results endpoint", statusErr)
	}
}
