package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"porter/controlplane/internal/store"
)

var upstreamTransport = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     false,
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   64,
	MaxConnsPerHost:       256,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: time.Second,
}

type Gateway struct {
	Store *store.Store
	rr    uint64
}

func New(st *store.Store) *Gateway { return &Gateway{Store: st} }

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets net/http.ResponseController preserve streaming and upgrade support.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := r.Header.Get("X-Request-ID")
	if !validTraceID(traceID) {
		traceID = newTraceID()
	}
	w.Header().Set("X-Request-ID", traceID)

	host := normalizeHost(r.Host)
	route, ok := g.Store.RouteForHost(r.Context(), host)
	if !ok {
		http.Error(w, "no Porter route for host "+host, http.StatusNotFound)
		return
	}
	upstreamPath, ok := stripPathPrefix(r.URL.Path, route.PathPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	port, ok := g.Store.AppPort(r.Context(), route.AppID)
	if !ok {
		http.Error(w, "route target app missing", http.StatusBadGateway)
		return
	}
	replicas := g.Store.HealthyReplicasForApp(r.Context(), route.AppID)
	if len(replicas) == 0 {
		http.Error(w, "no healthy replicas", http.StatusServiceUnavailable)
		return
	}
	totalWeight := 0
	for _, endpoint := range replicas {
		if endpoint.Weight <= 0 {
			endpoint.Weight = 1
		}
		totalWeight += endpoint.Weight
	}
	pick := int(atomic.AddUint64(&g.rr, 1) % uint64(totalWeight))
	selected := replicas[0]
	for _, endpoint := range replicas {
		weight := endpoint.Weight
		if weight <= 0 {
			weight = 1
		}
		if pick < weight {
			selected = endpoint
			break
		}
		pick -= weight
	}
	if port <= 0 {
		port = 8080
	}
	parsedIP := net.ParseIP(selected.IP)
	if parsedIP == nil || !parsedIP.IsPrivate() || port > 65535 {
		http.Error(w, "invalid private upstream endpoint", http.StatusBadGateway)
		return
	}
	target, err := url.Parse("http://" + net.JoinHostPort(parsedIP.String(), strconv.Itoa(port)))
	if err != nil {
		http.Error(w, "invalid upstream endpoint", http.StatusBadGateway)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = upstreamTransport
	proxy.FlushInterval = -1
	originalDirector := proxy.Director
	proxy.Director = func(out *http.Request) {
		originalDirector(out)
		out.URL.Path = upstreamPath
		out.URL.RawPath = ""
		out.Header.Set("X-Request-ID", traceID)
		out.Header.Set("X-Forwarded-Host", r.Host)
		if r.TLS != nil {
			out.Header.Set("X-Forwarded-Proto", "https")
		} else {
			out.Header.Set("X-Forwarded-Proto", "http")
		}
		if clientIP := remoteIP(r.RemoteAddr); clientIP != "" {
			out.Header.Set("X-Real-IP", clientIP)
			out.Header.Set("X-Forwarded-For", clientIP)
		} else {
			out.Header.Del("X-Forwarded-For")
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("gateway upstream %s: %v", target, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	rec := &recorder{ResponseWriter: w}
	proxy.ServeHTTP(rec, r)
	g.Store.RecordRequest(context.Background(), traceID, host, r.URL.Path, r.Method, rec.status, int(time.Since(start).Milliseconds()), rec.bytes, selected.ReplicaID)
	_ = g.Store.TouchApp(context.Background(), route.AppID, 0)
}

func normalizeHost(hostPort string) string {
	host := strings.TrimSpace(hostPort)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func stripPathPrefix(path, prefix string) (string, bool) {
	if prefix == "" || prefix == "/" {
		return path, true
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", false
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if path == prefix {
		return "/", true
	}
	if !strings.HasPrefix(path, prefix+"/") {
		return "", false
	}
	return strings.TrimPrefix(path, prefix), true
}

func validTraceID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

func newTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "tr_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "tr_" + hex.EncodeToString(b)
}

func remoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return strings.Trim(remoteAddr, "[]")
}
