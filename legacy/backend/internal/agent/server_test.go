package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlAuth(t *testing.T) {
	s := &Server{ControlToken: "ctrl", ProxyToken: "proxy"}
	h := s.ControlHandler()
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token must 401, got %d", w.Code)
	}
	req.Header.Set("Authorization", "Bearer ctrl")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid bearer must 200, got %d", w.Code)
	}
	empty := &Server{}
	w = httptest.NewRecorder()
	empty.ControlHandler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatal("empty token must fail closed")
	}
}

func TestProxyAuth(t *testing.T) {
	s := &Server{ControlToken: "ctrl", ProxyToken: "proxy"}
	h := s.ProxyHandler()
	// Header path.
	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("X-Proxy-Token", "proxy")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid proxy token must 200, got %d", w.Code)
	}
	// Query fallback is WebSocket-only: plain GET with ?token= stays 401.
	q := httptest.NewRequest("GET", "/health?token=proxy", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, q)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("query token on plain GET must 401")
	}
	// WebSocket upgrade may use ?token=.
	ws := httptest.NewRequest("GET", "/health?token=proxy", nil)
	ws.Header.Set("Connection", "Upgrade")
	ws.Header.Set("Upgrade", "websocket")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, ws)
	if w.Code != http.StatusOK {
		t.Fatalf("ws query token must 200, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if len(body) == 0 {
		t.Fatal("health must answer")
	}
}
