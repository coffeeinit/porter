package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveStartupSecurityRequiresExplicitAdminCredential(t *testing.T) {
	security, err := resolveStartupSecurity("", "operator-secret")
	if err != nil || security.APIMode != apiModeProduction || security.AdminToken != "operator-secret" {
		t.Fatalf("default startup security=(%+v,%v)", security, err)
	}
	security, err = resolveStartupSecurity(" DEVELOPMENT ", " operator-secret ")
	if err == nil {
		t.Fatal("admin token with surrounding whitespace was accepted")
	}
	security, err = resolveStartupSecurity(" DEVELOPMENT ", "operator-secret")
	if err != nil || security.APIMode != apiModeDevelopment || security.AdminToken != "operator-secret" {
		t.Fatalf("explicit development security=(%+v,%v)", security, err)
	}
	for _, tc := range []struct{ mode, token string }{
		{mode: "production"},
		{mode: "development", token: " \t"},
		{mode: "development", token: "operator secret"},
		{mode: "unsafe", token: "operator-secret"},
	} {
		if _, err := resolveStartupSecurity(tc.mode, tc.token); err == nil {
			t.Errorf("resolveStartupSecurity(%q,%q) unexpectedly succeeded", tc.mode, tc.token)
		}
	}
}

func TestValidateAPIServerTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
		tls  bool
		mode string
		want bool
	}{
		{name: "production requires TLS", addr: ":8080", mode: apiModeProduction, want: true},
		{name: "production TLS", addr: ":8443", tls: true, mode: apiModeProduction},
		{name: "loopback development", addr: "127.0.0.1:8080", mode: apiModeDevelopment},
		{name: "IPv6 loopback development", addr: "[::1]:8080", mode: apiModeDevelopment},
		{name: "localhost development", addr: "localhost:8080", mode: apiModeDevelopment},
		{name: "non-loopback plaintext rejected", addr: "0.0.0.0:8080", mode: apiModeDevelopment, want: true},
		{name: "malformed listener rejected", addr: ":", mode: apiModeDevelopment, want: true},
		{name: "unknown mode rejected", addr: "127.0.0.1:8080", mode: "test", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAPIServerTransport(tc.addr, tc.tls, tc.mode)
			if (err != nil) != tc.want {
				t.Fatalf("validateAPIServerTransport() error=%v, wantError=%t", err, tc.want)
			}
		})
	}
}

func TestPostgresOnlyGatewaySettingFailsWhenEnabled(t *testing.T) {
	for _, value := range []string{"", "off", " OFF "} {
		if err := validatePostgresOnlyGatewaySetting(value); err != nil {
			t.Errorf("disabled gateway setting %q failed: %v", value, err)
		}
	}
	if err := validatePostgresOnlyGatewaySetting(":8081"); err == nil {
		t.Fatal("legacy gateway listener was silently enabled")
	}
}

func TestRequestTrackerDrainsActiveAndRejectsNewRequests(t *testing.T) {
	tracker := newRequestTracker()
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	handlerDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://porter.local/", nil))
		close(handlerDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request handler did not start")
	}

	tracker.StopAccepting()
	tracker.StopAccepting()
	waitDone := make(chan struct{})
	go func() {
		tracker.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("tracker reported drained while a request was still active")
	default:
	}

	close(release)
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("tracker did not drain after the active request completed")
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not return")
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://porter.local/after-shutdown", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after shutdown returned %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
