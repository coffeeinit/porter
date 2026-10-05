// Package gateway is the HTTP front door: it routes by Host header (Vercel-
// style stable + preview domains) to the healthy replica pool for a service,
// logs every request into an in-memory ring (the one piece of state that is
// deliberately NOT in Postgres — high write, low durability), and serves as a
// reverse proxy to the target microVM.
package gateway

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"porter/internal/types"
)

// Store is the narrow persistence surface the gateway needs. AddTraffic lets
// the dashboard's GET /traffic endpoints (which read store.ListTraffic) see
// live proxied requests, not just the in-memory ring.
type Store interface {
	GetVM(id string) (*types.VM, bool)
	ListVMs() []*types.VM
	ListDomains(vmID string) []*types.Domain
	// ListProjectDomains lists a project's domain records. Domains attach to
	// projects (store.AddDomain(projectID, ...)); the old per-VM call is kept
	// for backward-compatible fakes but never matches real rows.
	ListProjectDomains(projectID string) []*types.Domain
	ListFirewallRules(projectID string) []*types.FirewallRule
	AddTraffic(vmID string, e *types.TrafficEntry)
}

// deploymentStore is optional so older gateway test stores and integrations
// remain valid while the production PostgreSQL store enables weighted routing.
type deploymentStore interface {
	ListDeployments(projectID string) []*types.Deployment
}

// DNSResolver resolves <svc>.<project>.local hostnames to VM IPs; wired from
// the dns package when enabled.
type DNSResolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// TrafficRing is a bounded, in-memory per-VM request log.
type TrafficRing struct {
	mu    sync.RWMutex
	perVM map[string][]*types.TrafficEntry
	size  int
}

// NewTrafficRing returns a ring that keeps the last n entries per VM.
func NewTrafficRing(n int) *TrafficRing {
	if n <= 0 {
		n = 500
	}
	return &TrafficRing{perVM: map[string][]*types.TrafficEntry{}, size: n}
}

// Add appends one traffic entry to a VM's ring, trimming to the bound.
func (t *TrafficRing) Add(vmID string, e *types.TrafficEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	buf := append(t.perVM[vmID], e)
	if len(buf) > t.size {
		buf = buf[len(buf)-t.size:]
	}
	t.perVM[vmID] = buf
}

// List returns up to limit recent entries for a VM, oldest first.
func (t *TrafficRing) List(vmID string, limit int) []*types.TrafficEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	buf := t.perVM[vmID]
	if limit <= 0 || limit > len(buf) {
		limit = len(buf)
	}
	start := len(buf) - limit
	out := make([]*types.TrafficEntry, limit)
	copy(out, buf[start:])
	return out
}

// Gateway routes Host-header requests to VM pools.
type Gateway struct {
	store  Store
	ring   *TrafficRing
	logger *log.Logger
	dns    DNSResolver
	rr     atomic.Uint64 // round-robin cursor across the healthy replica pool
	// Waker boots a scaled-to-zero project on demand. It is called (at most
	// once per request) when a request matches a project's domain but the
	// project has zero healthy VMs. Nil (default) keeps the current behavior:
	// a bare 503 with no wake attempt.
	Waker func(ctx context.Context, projectID string) error
}

// NewGateway builds the gateway with its in-memory traffic ring.
func NewGateway(store Store) *Gateway {
	return &Gateway{
		store:  store,
		ring:   NewTrafficRing(500),
		logger: log.New(log.Writer(), "gateway: ", log.LstdFlags),
	}
}

// Ring exposes the traffic ring (fast path; the store is the dashboard source).
func (g *Gateway) Ring() *TrafficRing {
	return g.ring
}

// SetDNS attaches the .local resolver so hostnames that don't match a domain
// record can still be routed to the right replica pool.
func (g *Gateway) SetDNS(r DNSResolver) { g.dns = r }

// ServeHTTP implements http.Handler: resolve the Host header to a VM backend
// and reverse-proxy, then record a traffic entry to the ring AND the store.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.Split(r.Host, ":")[0]

	vms := g.backendsFor(host)
	if len(vms) == 0 {
		// Scale-to-zero wake: the host belongs to a project whose VMs are
		// all down. Boot it on demand, then tell the client to retry — the
		// boot races the response, it never blocks it.
		if projectID, ok := g.projectForHost(host); ok && !g.projectHasHealthy(projectID) && g.Waker != nil {
			status := "started"
			if err := g.Waker(r.Context(), projectID); err != nil {
				status = "failed"
				g.logger.Printf("gateway: wake project %s for %s: %v", projectID, host, err)
			}
			w.Header().Set("Retry-After", "5")
			w.Header().Set("X-Porter-Wake", status)
		}
		http.Error(w, "no healthy backend for "+host, http.StatusServiceUnavailable)
		return
	}
	// A deployment cookie keeps a user on one version for the duration of an
	// experiment. Users without a cookie use the weighted pool.
	var vm *types.VM
	if cookie, err := r.Cookie("porter_deployment"); err == nil {
		for _, candidate := range vms {
			if candidate.DeploymentID == cookie.Value || candidate.DeploymentVersion == cookie.Value {
				vm = candidate
				break
			}
		}
	}
	if vm == nil {
		vm = vms[int(g.rr.Add(1))%len(vms)]
		if vm.DeploymentVersion != "" {
			http.SetCookie(w, &http.Cookie{Name: "porter_deployment", Value: vm.DeploymentVersion, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
	}

	// Firewall: enforce the project's active deny rules before proxying.
	if rule := g.blockedByFirewall(vm.ProjectID, r); rule != nil {
		g.logger.Printf("firewall: blocked %s %s from %s by rule %s (%s)", r.Method, r.URL.Path, r.RemoteAddr, rule.ID, rule.Source)
		http.Error(w, "blocked by firewall", http.StatusForbidden)
		return
	}

	target, ok := targetAddress(vm)
	if !ok {
		http.Error(w, "vm has no address", http.StatusServiceUnavailable)
		return
	}

	rec := &responseRecorder{ResponseWriter: w, status: 200}
	// Wrap the request body in a counting reader so uploads report real bytes.
	reqIn := &countingReader{r: r.Body}
	if r.Body != nil {
		r.Body = reqIn
	}
	start := time.Now()
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(rec, r)
	dur := time.Since(start).Milliseconds()

	entry := &types.TrafficEntry{
		Timestamp:  start,
		ProjectID:  vm.ProjectID,
		Method:     r.Method,
		Host:       r.Host,
		Path:       r.URL.Path,
		Status:     rec.status,
		DurationMS: int(dur),
		RemoteIP:   r.RemoteAddr,
		BytesIn:    reqIn.n,
		BytesOut:   rec.bytes,
	}
	g.ring.Add(vm.ID, entry)
	g.store.AddTraffic(vm.ID, entry)
}

// blockedByFirewall returns the first active deny rule that matches the
// request (by source IP/CIDR), or nil when the request is allowed. Rules with
// a Source are matched as exact IP or CIDR; empty-source deny rules apply to
// every request for the project. Order follows Priority (lower = applied first),
// matching a sorted rule evaluation.
func (g *Gateway) blockedByFirewall(projectID string, r *http.Request) *types.FirewallRule {
	if projectID == "" {
		return nil
	}
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = h
	}
	// Evaluate deny rules highest-priority first (lower Priority value = higher).
	rules := g.store.ListFirewallRules(projectID)
	best := []*types.FirewallRule{}
	for _, fr := range rules {
		if fr == nil || !fr.Active || fr.Action != "deny" {
			continue
		}
		best = append(best, fr)
	}
	for i := 0; i < len(best); i++ {
		for j := i + 1; j < len(best); j++ {
			if best[j].Priority < best[i].Priority {
				best[i], best[j] = best[j], best[i]
			}
		}
	}
	for _, fr := range best {
		if sourceMatches(fr.Source, ip) {
			return fr
		}
	}
	return nil
}

// sourceMatches checks an exact IP or CIDR match (empty source matches all).
func sourceMatches(source, ip string) bool {
	if source == "" || source == "*" || source == "0.0.0.0/0" {
		return true
	}
	if source == ip {
		return true
	}
	if _, cidr, err := net.ParseCIDR(source); err == nil {
		parsed := net.ParseIP(ip)
		return parsed != nil && cidr.Contains(parsed)
	}
	return false
}

// backendsFor finds healthy VMs that serve the given domain: exact match on
// a project's domain records (custom/stable) or a deployment preview URL,
// then a DNS-resolution pass for *.local service names, then fall back to
// any running VM (dev convenience).
func (g *Gateway) backendsFor(host string) []*types.VM {
	// Preview deployments first: an exact preview host belongs to exactly
	// one deployment pool (never the weighted production mix).
	if ds, ok := g.store.(deploymentStore); ok {
		seen := map[string]bool{}
		for _, vm := range g.store.ListVMs() {
			if seen[vm.ProjectID] {
				continue
			}
			seen[vm.ProjectID] = true
			for _, d := range ds.ListDeployments(vm.ProjectID) {
				if d == nil || d.PreviewURL == "" || d.BuildStatus == "failed" {
					continue
				}
				if strings.EqualFold(d.PreviewURL, host) {
					out := []*types.VM{}
					for _, vmID := range d.VMIDs {
						if vm, exists := g.store.GetVM(vmID); exists && isHealthy(vm) {
							out = append(out, vm)
						}
					}
					if len(out) > 0 {
						return out
					}
				}
			}
		}
	}

	// Project domain records (custom/stable).
	for _, vm := range g.store.ListVMs() {
		if !isHealthy(vm) {
			continue
		}
		for _, d := range g.store.ListProjectDomains(vm.ProjectID) {
			if strings.EqualFold(d.Domain, host) {
				return g.weightedDeploymentBackends(vm.ProjectID, vm)
			}
		}
		// Legacy per-VM records (older fakes/tests).
		for _, d := range g.store.ListDomains(vm.ID) {
			if strings.EqualFold(d.Domain, host) {
				return g.weightedDeploymentBackends(vm.ProjectID, vm)
			}
		}
	}

	// <svc>.<project>.local — resolve through the attached dns resolver and
	// pick the healthy VM whose IP matches.
	if g.dns != nil && strings.HasSuffix(host, ".local") {
		if ips, err := g.dns.LookupIP(context.Background(), host); err == nil {
			for _, vm := range g.store.ListVMs() {
				if !isHealthy(vm) {
					continue
				}
				for _, ip := range ips {
					if vm.IPAddress != "" && vm.IPAddress == ip.String() {
						return []*types.VM{vm}
					}
				}
			}
		}
	}
	// Fallback: single running VM answers any host (single-tenant dev path).
	out := []*types.VM{}
	for _, vm := range g.store.ListVMs() {
		if isHealthy(vm) {
			out = append(out, vm)
		}
	}
	return out
}

func isHealthy(vm *types.VM) bool {
	return vm.State == types.StateRunning && vm.HealthStatus != types.HealthUnhealthy
}

// projectForHost resolves a Host header to the owning project by scanning
// every VM (healthy or not — scaled-to-zero projects have no healthy VMs)
// for a matching project-domain or legacy per-VM domain record.
func (g *Gateway) projectForHost(host string) (string, bool) {
	for _, vm := range g.store.ListVMs() {
		if vm == nil || vm.ProjectID == "" {
			continue
		}
		for _, d := range g.store.ListProjectDomains(vm.ProjectID) {
			if d != nil && strings.EqualFold(d.Domain, host) {
				return vm.ProjectID, true
			}
		}
		for _, d := range g.store.ListDomains(vm.ID) {
			if d != nil && strings.EqualFold(d.Domain, host) {
				return vm.ProjectID, true
			}
		}
	}
	return "", false
}

// projectHasHealthy reports whether the project has at least one healthy VM.
func (g *Gateway) projectHasHealthy(projectID string) bool {
	for _, vm := range g.store.ListVMs() {
		if vm != nil && vm.ProjectID == projectID && isHealthy(vm) {
			return true
		}
	}
	return false
}

// statusCode peeks at the response status code committed so far (best effort).
func statusCode(w http.ResponseWriter) int {
	if rw, ok := w.(*responseRecorder); ok {
		return rw.status
	}
	return 200
}

// responseRecorder decorates a ResponseWriter to capture a status code and the
// number of response bytes written (wrapped around the real writer by callers
// that want traffic status + bandwidth).
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rw *responseRecorder) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseRecorder) Write(p []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(p)
	rw.bytes += int64(n)
	return n, err
}

// Bytes returns the number of response bytes written so far.
func (rw *responseRecorder) Bytes() int64 { return rw.bytes }

// NewResponseWriter wraps w with a status + byte recorder.
func NewResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	return &responseRecorder{ResponseWriter: w, status: 200}
}

// countingReader wraps an io.ReadCloser (e.g. a request body) and counts the
// total number of bytes read through it — the request side of byte-level
// bandwidth. Close is forwarded so the request body lifecycle is unchanged.
type countingReader struct {
	r io.ReadCloser
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) Close() error { return c.r.Close() }

// targetAddress builds the upstream URL for a VM from its IP and the first
// container port (the port the app listens on *inside* the VM). HostPort is
// the host-facing binding handled by the PortForwarder; it is never used as
// the gateway's upstream port.
func targetAddress(vm *types.VM) (*url.URL, bool) {
	if vm.IPAddress == "" {
		return nil, false
	}
	port := 80
	if len(vm.Ports) > 0 && vm.Ports[0].ContainerPort != 0 {
		port = vm.Ports[0].ContainerPort
	}
	return &url.URL{Scheme: "http", Host: vm.IPAddress + ":" + itoa(port)}, true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

var _ = httputil.ReverseProxy{}
var _ = url.URL{}

// weightedDeploymentBackends returns healthy replicas grouped by deployment
// route weight. It is intentionally bounded to 100 entries so a percentage
// rollout cannot create unbounded per-request allocations.
func (g *Gateway) weightedDeploymentBackends(projectID string, fallback *types.VM) []*types.VM {
	ds, ok := g.store.(deploymentStore)
	if !ok {
		return []*types.VM{fallback}
	}
	deployments := ds.ListDeployments(projectID)
	out := make([]*types.VM, 0, 16)
	for _, d := range deployments {
		weight := d.RouteWeight
		if weight <= 0 {
			weight = d.RolloutPercent
		}
		if weight <= 0 || d.BuildStatus == "failed" || len(d.VMIDs) == 0 {
			continue
		}
		for _, vmID := range d.VMIDs {
			vm, exists := g.store.GetVM(vmID)
			if !exists || !isHealthy(vm) {
				continue
			}
			repeats := weight / 10
			if repeats < 1 {
				repeats = 1
			}
			if repeats > 10 {
				repeats = 10
			}
			for i := 0; i < repeats; i++ {
				out = append(out, vm)
			}
		}
	}
	if len(out) == 0 {
		return []*types.VM{fallback}
	}
	return out
}
