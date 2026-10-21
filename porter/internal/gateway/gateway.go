package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"porter/controlplane/internal/store"
)

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

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	traceID := r.Header.Get("X-Request-ID")
	if traceID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		traceID = "tr_" + hex.EncodeToString(b)
	}
	w.Header().Set("X-Request-ID", traceID)
	host := strings.Split(r.Host, ":")[0]
	route, ok := g.Store.RouteForHost(r.Context(), host)
	if !ok {
		http.Error(w, "no Porter route for host "+host, http.StatusNotFound)
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
	for _, e := range replicas {
		if e.Weight <= 0 {
			e.Weight = 1
		}
		totalWeight += e.Weight
	}
	pick := int(atomic.AddUint64(&g.rr, 1) % uint64(totalWeight))
	ip := replicas[0].IP
	replicaID := replicas[0].ReplicaID
	for _, e := range replicas {
		w := e.Weight
		if w <= 0 {
			w = 1
		}
		if pick < w {
			ip = e.IP
			replicaID = e.ReplicaID
			break
		}
		pick -= w
	}
	if port <= 0 {
		port = 8080
	}
	target, _ := url.Parse("http://" + ip + ":" + strconv.Itoa(port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("gateway upstream %s: %v", target, err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	rec := &recorder{ResponseWriter: w}
	proxy.ServeHTTP(rec, r)
	g.Store.RecordRequest(context.Background(), traceID, host, r.URL.Path, r.Method, rec.status, int(time.Since(start).Milliseconds()), rec.bytes, replicaID)
	_ = g.Store.TouchApp(context.Background(), route.AppID, 0)
}
