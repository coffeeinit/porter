// Porter agent mode: host agent with a dual-port HTTP surface.
//
// Control plane enrolls the host out of band (dashboard Servers view); the
// agent then reports liveness on a heartbeat loop and serves:
//
//	:9090 control — Bearer-gated operator surface. Bind firewalled; never
//	        expose publicly (see agent.Server.ControlHandler).
//	:9091 proxy   — loopback-only data proxy + terminal sessions backed by a
//	        ptyd.Registry. The gateway tunnel is the sole remote path in.
//
// Enrollment: no server-side enroll endpoint exists (only POST
// /nodes/enrollment-tokens to mint a token and POST /servers to register),
// so enroll here is local: validate PORTER_ENROLL_TOKEN, persist agent.env,
// and log. Heartbeats POST the control plane's /servers/{id}/heartbeat shape
// (types.ServerHeartbeat — verified against handleServerHeartbeat); unknown
// servers 404 until an operator registers them, logged loudly, never fatal.
//
// All gates fail closed: empty enroll/control/proxy tokens exit non-zero
// before serving anything.
package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"porter/internal/ptyd"
	"porter/internal/types"

	"runtime"
	"strconv"
)

const (
	agentHeartbeatInterval = 30 * time.Second
	agentShutdownTimeout   = 10 * time.Second
	agentEnvFile           = "agent.env"
)

func Run(version string) int {
	enrollToken := os.Getenv("PORTER_ENROLL_TOKEN")
	controlURL := envOr("PORTER_CONTROL_URL", "http://127.0.0.1:8080")
	controlToken := os.Getenv("PORTER_AGENT_CONTROL_TOKEN")
	proxyToken := os.Getenv("PORTER_AGENT_PROXY_TOKEN")
	nodeID := envOr("PORTER_AGENT_ID", defaultNodeID())

	// Fail closed: no token, no agent. An agent serving its control or proxy
	// surface without a token would accept unauthenticated operators.
	if enrollToken == "" {
		log.Fatalf("agent: PORTER_ENROLL_TOKEN is required (fail-closed)")
	}
	if err := ValidateToken(enrollToken); err != nil {
		log.Fatalf("agent: bad enroll token: %v", err)
	}
	if controlToken == "" {
		log.Fatalf("agent: PORTER_AGENT_CONTROL_TOKEN is required (fail-closed)")
	}
	if proxyToken == "" {
		log.Fatalf("agent: PORTER_AGENT_PROXY_TOKEN is required (fail-closed)")
	}

	// Server-side enrollment: exchange the one-time enrollment token for a
	// persistent node identity (server row + node token for heartbeats).
	serverID := os.Getenv("PORTER_SERVER_ID")
	nodeToken := os.Getenv("PORTER_NODE_TOKEN")
	if serverID == "" || nodeToken == "" {
		sid, tok, err := enrollNode(controlURL, enrollToken, nodeID)
		if err != nil {
			log.Fatalf("agent: enroll via %s: %v", controlURL, err)
		}
		serverID, nodeToken = sid, tok
		log.Printf("agent: enrolled node %q server=%s", nodeID, serverID)
	}
	if err := writeAgentEnv(agentEnvFile, controlURL, nodeID, serverID, nodeToken); err != nil {
		log.Fatalf("agent: write %s: %v", agentEnvFile, err)
	}

	reg := ptyd.NewRegistry(4)
	ag := &Server{
		ControlToken: controlToken,
		ProxyToken:   proxyToken,
		Info: func() map[string]any {
			return map[string]any{"node_id": nodeID, "version": version}
		},
	}

	// Proxy side: agent proxy handler plus terminal sessions on the same
	// loopback listener, both behind the proxy token.
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/", ag.ProxyHandler())
	proxyMux.Handle("POST /terminal/sessions", agentProxyGate(proxyToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var s ptyd.Session
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writeAgentJSON(w, http.StatusBadRequest, map[string]string{"error": "bad session body"})
			return
		}
		if s.OpenedAt.IsZero() {
			s.OpenedAt = time.Now()
		}
		if s.Principal == "" {
			s.Principal = "agent:" + nodeID
		}
		if err := reg.Open(s); err != nil {
			writeAgentJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("agent: terminal session opened id=%q vm=%q by=%q", s.ID, s.VMID, s.Principal)
		writeAgentJSON(w, http.StatusCreated, map[string]string{"id": s.ID})
	})))
	proxyMux.Handle("DELETE /terminal/sessions/{id}", agentProxyGate(proxyToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		reg.Close(id) // idempotent: unknown IDs are a no-op
		log.Printf("agent: terminal session closed id=%q", id)
		writeAgentJSON(w, http.StatusOK, map[string]string{"status": "closed"})
	})))

	controlAddr, proxyAddr := ListenAddrs()
	controlSrv := &http.Server{
		Addr:              controlAddr,
		Handler:           ag.ControlHandler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	proxySrv := &http.Server{
		Addr:              proxyAddr,
		Handler:           proxyMux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go agentHeartbeatLoop(ctx, controlURL, serverID, nodeToken, version)

	go func() {
		log.Printf("agent: control on %s (firewalled — never expose publicly)", controlAddr)
		if err := controlSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("agent: control server error: %v", err)
		}
	}()
	go func() {
		log.Printf("agent: proxy on %s (loopback only; gateway tunnel is the sole remote path)", proxyAddr)
		if err := proxySrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("agent: proxy server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("agent: shutting down...")
	// Independent bounded joins: each listener gets the full shutdown
	// timeout so a hung keep-alive on control never starves the proxy
	// drain (shared-context expiry would fail the second Shutdown).
	shutC, cancelC := context.WithTimeout(context.Background(), agentShutdownTimeout)
	if err := controlSrv.Shutdown(shutC); err != nil && err != http.ErrServerClosed {
		log.Printf("agent: control shutdown error: %v", err)
	}
	cancelC()
	shutP, cancelP := context.WithTimeout(context.Background(), agentShutdownTimeout)
	if err := proxySrv.Shutdown(shutP); err != nil && err != http.ErrServerClosed {
		log.Printf("agent: proxy shutdown error: %v", err)
	}
	cancelP()
	log.Printf("agent: stopped")
	return 0
}

// agentHeartbeatLoop reports liveness to the control plane until ctx ends.
// Failures are logged, never fatal: a partitioned agent keeps serving its
// local surfaces and retries on the next tick.
func agentHeartbeatLoop(ctx context.Context, controlURL, serverID, nodeToken, version string) {
	url := strings.TrimRight(controlURL, "/") + "/api/v1/servers/" + serverID + "/heartbeat"
	client := &http.Client{Timeout: 10 * time.Second}
	beat := func() {
		hb := types.ServerHeartbeat{
			ID:      serverID,
			Status:  "online",
			Version: version,
			VCPUs:   runtime.NumCPU(),
			MemMiB:  hostMemTotalMiB(),
			OS:      hostOSName(),
			Arch:    runtime.GOARCH,
		}
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(hb); err != nil {
			log.Printf("agent: heartbeat encode: %v", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
		if err != nil {
			log.Printf("agent: heartbeat request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+nodeToken)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("agent: heartbeat POST %s: %v (register this node via POST /servers first)", url, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("agent: heartbeat POST %s: status %d (register this node via POST /servers first)", url, resp.StatusCode)
			return
		}
		log.Printf("agent: heartbeat ok (server %q)", serverID)
	}

	beat()
	t := time.NewTicker(agentHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}

// agentProxyGate enforces the proxy token on the terminal endpoints added
// alongside agent.Server.ProxyHandler. Empty expected token fails closed.
func agentProxyGate(proxyToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proxyToken == "" {
			http.Error(w, "agent proxy disabled", http.StatusInternalServerError)
			return
		}
		got := r.Header.Get("X-Proxy-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(proxyToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeAgentEnv persists the local enrollment record.
func writeAgentEnv(path, controlURL, nodeID, serverID, nodeToken string) error {
	body := fmt.Sprintf("PORTER_CONTROL_URL=%s\nPORTER_AGENT_ID=%s\nPORTER_SERVER_ID=%s\nPORTER_NODE_TOKEN=%s\nPORTER_ENROLLED_AT=%s\n",
		controlURL, nodeID, serverID, nodeToken, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(path, []byte(body), 0o600)
}

// defaultNodeID prefers the OS hostname, falling back to a stable literal.
func defaultNodeID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "agent-1"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// enrollNode exchanges a one-time enrollment token for a persistent node
// identity: server row + per-node heartbeat token (shown once, hash stored
// server-side). Returns (serverID, nodeToken).
func enrollNode(controlURL, enrollToken, nodeID string) (string, string, error) {
	body, err := json.Marshal(map[string]string{
		"token":    enrollToken,
		"hostname": nodeID,
		"node_id":  nodeID,
	})
	if err != nil {
		return "", "", err
	}
	url := strings.TrimRight(controlURL, "/") + "/api/v1/nodes/enroll"
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		Server    *types.Server `json:"server"`
		NodeID    string        `json:"node_id"`
		NodeToken string        `json:"node_token"`
		Heartbeat string        `json:"heartbeat_path"`
		Error     string        `json:"error"`
		Message   string        `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("status %d: decode: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusCreated || out.Server == nil || out.NodeToken == "" {
		msg := out.Error
		if out.Message != "" {
			msg += ": " + out.Message
		}
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, msg)
	}
	return out.Server.ID, out.NodeToken, nil
}

// hostMemTotalMiB reads MemTotal from /proc/meminfo; 0 when unreadable
// (heartbeats stay truthful: no value is better than a wrong one).
func hostMemTotalMiB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.Atoi(f[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// hostOSName returns the PRETTY_NAME from /etc/os-release, falling back to
// the kernel name.
func hostOSName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
		}
	}
	return "linux"
}
