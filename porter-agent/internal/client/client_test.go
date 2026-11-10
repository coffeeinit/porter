package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestJoinRejectsRemotePlainHTTPBeforeConnecting(t *testing.T) {
	_, err := Join(context.Background(), "http://control.example.com:8080", protocol.EnrollRequest{Token: "join-token"})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Join error = %v, want remote HTTP rejected with HTTPS guidance", err)
	}
}

func TestClientRejectsRemotePlainHTTPBeforeSendingNodeToken(t *testing.T) {
	cl := New("http://control.example.com:8080", "node-1", "node-secret")
	err := cl.Heartbeat(context.Background(), protocol.Heartbeat{NodeID: "node-1"})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Heartbeat error = %v, want remote HTTP rejected with HTTPS guidance", err)
	}
}

func TestResultReturnsTypedHTTPStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	cl := New(server.URL, "node-1", "node-token")
	err := cl.Result(context.Background(), protocol.CommandResult{
		CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-1", Attempt: 1,
	})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Result error = %v, want *HTTPStatusError", err)
	}
	if statusErr.HTTPStatusCode() != http.StatusConflict || statusErr.Path != "/v1/fleet/result" {
		t.Fatalf("HTTP status error = %+v, want conflict on result endpoint", statusErr)
	}
}
