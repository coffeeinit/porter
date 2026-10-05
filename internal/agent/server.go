// Agent dual-port HTTP surface (OCM-07): :9090 control (firewalled, Bearer
// agent token) vs :9091 proxy (loopback-only, X-Proxy-Token). Both fail
// closed on empty tokens; proxy tokens compare in constant time and never
// ride the URL (headers only; query fallback exists for WebSocket only).
package agent

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Server serves the agent control + proxy routers. Tokens are expected
// values, never accepted empty (empty = 500 fail-closed, OCM-07).
type Server struct {
	ControlToken string
	ProxyToken   string
	Info         func() map[string]any
}

// bearerGate enforces the control Bearer token.
func (s *Server) bearerGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ControlToken == "" {
			http.Error(w, "agent control disabled", http.StatusInternalServerError)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.ControlToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// proxyGate enforces the proxy token from the header (or ?token= for
// WebSocket upgrades only, which cannot set headers cross-origin).
func (s *Server) proxyGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.ProxyToken == "" {
			http.Error(w, "agent proxy disabled", http.StatusInternalServerError)
			return
		}
		got := r.Header.Get("X-Proxy-Token")
		if got == "" && isWebSocket(r) {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.ProxyToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isWebSocket(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func writeAgentJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ControlHandler builds the :9090 router. Bind firewalled; never public.
func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeAgentJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		info := map[string]any{"control_port": ControlPort, "proxy_port": ProxyPort}
		if s.Info != nil {
			for k, v := range s.Info() {
				info[k] = v
			}
		}
		writeAgentJSON(w, http.StatusOK, info)
	})
	return s.bearerGate(mux)
}

// ProxyHandler builds the :9091 router. Bind 127.0.0.1 only; the gateway
// tunnel is the sole remote path.
func (s *Server) ProxyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeAgentJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	return s.proxyGate(mux)
}

// ListenAddrs returns the conventional bind addresses (loopback for proxy).
func ListenAddrs() (control, proxy string) {
	return fmt.Sprintf(":%d", ControlPort), fmt.Sprintf("127.0.0.1:%d", ProxyPort)
}
