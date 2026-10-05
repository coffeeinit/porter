// Package api — full implementation of every route registered in api.go.
//
// Domains: auth/account, orgs/teams/groups, resellers/customers, projects,
// scale/health, env/secrets, domains/DNS/zones/records/certificates,
// gateways/LB/port-forwards, compose, deployments/rollout/freeze/previews,
// replicas, settings, environments, hooks, crons, drains, alerts, incidents,
// SLOs, synthetics, redirects, analytics, firewall, cache, volumes,
// storage-classes, object-stores, buckets, backups/policies, snapshots,
// images/registry/vCenter, overview/host/logs/traffic, servers/users/export,
// nodes/enrollment, providers/regions/zones/node-pools/capacity,
// billing (plans/products/prices/entitlements/subscriptions/invoices/
// payments/credits/refunds/tax/dunning), workflows/webhooks/maintenance,
// AI agents/plans/MCP/impersonation, extensions/marketplace, templates/shares/
// email/Cloudflare, stacks/VPS/kernel/bench, gitops webhooks, K8s (extension),
// Operator admin surface, databases.
//
// Every handler: parse req → validate → store/runtime → writeJSON. Runtime ops
// go through a.vmm; observability through the store's in-memory rings.

package api

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	assets "porter"
	"porter/internal/store"
)

var osHostname = os.Hostname

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// notImplemented writes a consistent 501 envelope for subsystems that are
// genuinely not wired yet (SRS §66 rule 11 — never fake unimplemented).
//
// The body carries the same code/message/error/request_id keys as writeError,
// so every client reaches the explanation through the one error path it
// already has — the dashboard, the CLI, an SDK and an AI agent all rendered
// only "HTTP 501" while this envelope used to expose the explanation solely
// under "reason". status/reason/feature are kept for callers that want the
// machine-readable detail.
func notImplemented(w http.ResponseWriter, feature string) {
	msg := feature + " is not wired in this build; the route exists for forward compatibility and is tracked for a later release"
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"code":       http.StatusNotImplemented,
		"error":      msg,
		"message":    msg,
		"request_id": w.Header().Get("X-Request-ID"),
		"status":     "unsupported",
		"reason":     msg,
		"feature":    feature,
	})
}

// listFromStore is a tiny generic helper for list endpoints backed by a
// slice-returning store method; keeps the new handlers compact.
func listFromStore[T any](w http.ResponseWriter, r *http.Request, key string, items []T) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, map[string]any{key: paginate(w, r, items)})
}

// decodeBody is a compact body decoder for the new handlers.
func decodeBody(r *http.Request, v any) error { return readJSON(r, v) }

func newID() string { return store.NewID() }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:2*n]
	}
	return hex.EncodeToString(b)
}

func tailN(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if n <= 0 {
		return 200
	}
	return n
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shortHash(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

type logStreamPayload struct {
	Source string   `json:"source"`
	Lines  []string `json:"lines"`
	Status string   `json:"status,omitempty"`
}

func serveLogStream(w http.ResponseWriter, r *http.Request, snapshot func() logStreamPayload, done func(string) bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	last := ""
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	send := func(payload logStreamPayload) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		key := string(body)
		if key == last {
			return true
		}
		last = key
		_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", body)
		flusher.Flush()
		return true
	}

	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		payload := snapshot()
		if !send(payload) {
			return
		}
		if done != nil && done(payload.Status) {
			_, _ = fmt.Fprint(w, "event: end\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return b
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (a *API) handleKernelBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := readJSON(r, &req); err != nil || req.Version == "" {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}
	opID, err := a.store.CreateOperationWithPayload("kernel-build", "kernel", req.Version, "kernel-build|"+req.Version,
		map[string]string{"version": req.Version})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record kernel build")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"op_id": opID, "state": "QUEUED"})
}

func (a *API) handleListBenchRuns(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": a.store.ListBenchRuns(r.URL.Query().Get("name"), limit)})
}

func (a *API) handleRecordBenchRun(w http.ResponseWriter, r *http.Request) {
	var req benchRunReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid bench body")
		return
	}
	id, err := a.store.RecordBenchRun(req.Name, req.MemoryMiB,
		time.Duration(req.SerialMs)*time.Millisecond,
		time.Duration(req.ShellMs)*time.Millisecond,
		time.Duration(req.AgentMs)*time.Millisecond, req.RSSMiB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "record bench run")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type benchRunReq struct {
	Name      string `json:"name"`
	MemoryMiB int    `json:"memory_mib"`
	SerialMs  int64  `json:"serial_ms"`
	ShellMs   int64  `json:"shell_ms"`
	AgentMs   int64  `json:"agent_ms"`
	RSSMiB    int64  `json:"rss_mib"`
}

// Misc (realtime, verify, magic link, onboarding, admin, subscription/new, tags, sources)

func (a *API) handleRealtime(w http.ResponseWriter, r *http.Request) {
	if a.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream is not configured")
		return
	}
	a.hub.ServeHTTP(w, r)
}

func (a *API) handleVerify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"verified": true, "user": currentUser(r)})
}

// ============================================================================
// Events stream + operation list + incidents (from earlier)
// ============================================================================

func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	if a.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream is not configured")
		return
	}
	a.hub.ServeHTTP(w, r)
}

func MountDashboard(mux *http.ServeMux) {
	if sub, err := fs.Sub(assets.Dist, "web/dist"); err == nil {
		mux.Handle("/", SpaFileServer(http.FS(sub)))
	} else {
		log.Printf("Dashboard assets not embedded; serving from ./web/dist if present")
		mux.Handle("/", SpaFileServer(http.Dir("./web/dist")))
	}
}

func SpaFileServer(files http.FileSystem) http.Handler {
	fileSrv := http.FileServer(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fileSrv.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" {
			fileSrv.ServeHTTP(w, r)
			return
		}
		if f, err := files.Open(pathToFile(r.URL.Path)); err == nil {
			if st, serr := f.Stat(); serr == nil && !st.IsDir() {
				_ = f.Close()
				fileSrv.ServeHTTP(w, r)
				return
			}
			_ = f.Close()
		}
		if hasFileExtension(r.URL.Path) {
			fileSrv.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, files)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, files http.FileSystem) {
	f, err := files.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "index.html", st.ModTime(), rs)
}

func pathToFile(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p = path.Join(p, "index.html")
	}
	return p
}

func hasFileExtension(p string) bool {
	base := path.Base(path.Clean("/" + p))
	dot := strings.LastIndex(base, ".")
	return dot > 0 && dot < len(base)-1
}

// recovered from internal/api/handlers.go
func execOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// recovered from internal/api/handlers.go
func execShell(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// recovered from internal/api/handlers.go
func orDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

// recovered from internal/api/feature_audit.go
func remoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}
	if net.ParseIP(remoteAddr) == nil {
		return ""
	}
	return remoteAddr
}

// recovered from internal/api/handlers.go
func safeGitURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", false
	}
	if strings.HasPrefix(u, "-") {
		return "", false
	}
	if strings.HasPrefix(u, "git@") {
		return u, true
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "", false
	}
	return u, true
}

// --- RECOVERED pass 2: helpers/types referenced by recovered handlers.
// --- Extracted verbatim from git HEAD; merge in place later.
// from handlers.go
func unzipTo(src io.Reader, dest string) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	cleanDest := filepath.Clean(dest)
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			continue // skip path-escape entries
		}
		target := filepath.Join(cleanDest, name)
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		of, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		_, _ = io.Copy(of, rc)
		of.Close()
		rc.Close()
	}
	return nil
}

// from feature_oci.go
func atoiDefault(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// from handlers.go
func (a *API) systemRole(roleID string) bool {
	return a.store.IsSystemRole(roleID)
}

// from feature_ranked.go
func (a *API) quotaCheck(projectID, key string, wouldBe int64) (int64, bool) {
	return store.OverQuota(a.projectQuotaLimits(projectID), key, wouldBe)
}

// from feature_ranked.go
func (a *API) projectQuotaLimits(projectID string) map[string]int64 {
	if projectID != "" {
		if planID, ok := a.store.ActiveSubscription(projectID); ok {
			return a.store.PlanLimits(planID)
		}
	}
	return a.store.PlanLimits("default")
}
