package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"porter/controlplane/internal/api"

	"porter/controlplane/internal/gateway"
	"porter/controlplane/internal/store"
	"porter/controlplane/internal/telemetry"
)

var Version = "v1.0.0-dev"

type gatewayTLSMode string

const (
	gatewayTLSHTTP   gatewayTLSMode = "http"
	gatewayTLSStatic gatewayTLSMode = "static"
	gatewayTLSACME   gatewayTLSMode = "acme"
)

func gatewayTLSConfig(certFile, keyFile, acmeDomains, acmeEnabled string) (gatewayTLSMode, []string, error) {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	acmeDomains = strings.TrimSpace(acmeDomains)
	acmeEnabled = strings.TrimSpace(acmeEnabled)
	hasCert, hasKey := certFile != "", keyFile != ""
	if hasCert != hasKey {
		return "", nil, fmt.Errorf("PORTER_TLS_CERT and PORTER_TLS_KEY must be set together")
	}
	if acmeEnabled != "" && !strings.EqualFold(acmeEnabled, "true") && !strings.EqualFold(acmeEnabled, "false") {
		return "", nil, fmt.Errorf("PORTER_ACME_ENABLED must be true or false")
	}
	if acmeDomains != "" || strings.EqualFold(acmeEnabled, "true") {
		if hasCert {
			return "", nil, fmt.Errorf("configure either PORTER_ACME_ENABLED/PORTER_ACME_DOMAINS or PORTER_TLS_CERT/PORTER_TLS_KEY, not both")
		}
		if acmeDomains == "" {
			return gatewayTLSACME, nil, nil
		}
		rawHosts := strings.Split(acmeDomains, ",")
		hosts := make([]string, 0, len(rawHosts))
		seen := make(map[string]struct{}, len(rawHosts))
		for _, host := range rawHosts {
			host = strings.TrimSpace(host)
			if host == "" {
				return "", nil, fmt.Errorf("PORTER_ACME_DOMAINS contains an empty hostname")
			}
			key := strings.ToLower(host)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			hosts = append(hosts, host)
		}
		return gatewayTLSACME, hosts, nil
	}
	if hasCert {
		return gatewayTLSStatic, nil, nil
	}
	return gatewayTLSHTTP, nil, nil
}

func acmeHostPolicy(st *store.Store, allowedHosts []string) func(context.Context, string) error {
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		host = normalizeACMEHost(host)
		if host != "" {
			allowed[host] = struct{}{}
		}
	}
	return func(ctx context.Context, host string) error {
		host = normalizeACMEHost(host)
		if host == "" {
			return fmt.Errorf("ACME host is empty")
		}
		if len(allowed) > 0 {
			if _, ok := allowed[host]; !ok {
				return fmt.Errorf("ACME host %q is not on the configured allowlist", host)
			}
		}
		if st == nil {
			return fmt.Errorf("ACME domain store is unavailable")
		}
		active, err := st.IsActiveRoutedDomain(ctx, host)
		if err != nil {
			return fmt.Errorf("check ACME domain %q: %w", host, err)
		}
		if !active {
			return fmt.Errorf("ACME host %q is not an active routed Porter domain", host)
		}
		return nil
	}
}

func normalizeACMEHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type requestTracker struct {
	mu       sync.Mutex
	active   int
	stopping bool
	drained  chan struct{}
}

func newRequestTracker() *requestTracker {
	return &requestTracker{drained: make(chan struct{})}
}

func (t *requestTracker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		if t.stopping {
			t.mu.Unlock()
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}
		t.active++
		t.mu.Unlock()

		defer func() {
			t.mu.Lock()
			t.active--
			if t.stopping && t.active == 0 {
				close(t.drained)
			}
			t.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (t *requestTracker) StopAccepting() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopping {
		return
	}
	t.stopping = true
	if t.active == 0 {
		close(t.drained)
	}
}

func (t *requestTracker) Wait() {
	<-t.drained
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Println("porter", Version)
			return
		case "server":
		case "status", "nodes", "projects", "deploy", "logs", "scale", "rollback", "join-token", "backup":
			os.Exit(runCLI(os.Args[1:]))
		default:
			fmt.Fprintln(os.Stderr, "usage: porter [server|version|status|nodes|projects|deploy|logs|scale|rollback|join-token|backup]")
			os.Exit(2)
		}
	}
	addr := env("PORTER_ADDR", ":8080")
	dbPath := env("PORTER_DB", "porter.db")
	gwAddr := env("PORTER_GATEWAY_ADDR", ":8081")
	certFile, keyFile, acmeDomains := os.Getenv("PORTER_TLS_CERT"), os.Getenv("PORTER_TLS_KEY"), os.Getenv("PORTER_ACME_DOMAINS")
	tlsMode, acmeHosts, tlsErr := gatewayTLSConfig(certFile, keyFile, acmeDomains, os.Getenv("PORTER_ACME_ENABLED"))
	if gwAddr != "" && gwAddr != "off" && tlsErr != nil {
		log.Fatalf("invalid gateway TLS configuration: %v", tlsErr)
	}
	apiTLSConfig, requireAgentMTLS, apiTLSErr := loadAPITLSConfig(os.Getenv("PORTER_API_TLS_CERT"), os.Getenv("PORTER_API_TLS_KEY"), os.Getenv("PORTER_AGENT_CLIENT_CA"))
	if apiTLSErr != nil {
		log.Fatalf("invalid API TLS configuration: %v", apiTLSErr)
	}
	admin := os.Getenv("PORTER_ADMIN_TOKEN")
	if admin == "" {
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		admin = "ptr_admin_" + hex.EncodeToString(b)
		fmt.Printf("generated admin token: %s\n", admin)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	requestContext, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	trackRequests := newRequestTracker()
	baseContext := func(net.Listener) context.Context { return requestContext }
	traceShutdown, err := telemetry.SetupTracing(context.Background())
	if err != nil {
		log.Fatalf("configure OpenTelemetry tracing: %v", err)
	}
	baseDomain := env("PORTER_BASE_DOMAIN", "porter.local")
	apiHandler := api.New(st, admin, baseDomain).Handler()
	if requireAgentMTLS {
		apiHandler = api.RequireAgentMTLS(apiHandler)
	}
	srv := &http.Server{Addr: addr, Handler: trackRequests.Wrap(telemetry.HTTPHandler(apiHandler, "porter.api")), TLSConfig: apiTLSConfig, ReadHeaderTimeout: 10 * time.Second, BaseContext: baseContext}
	go func() {
		var serveErr error
		if apiTLSConfig != nil {
			log.Printf("porter %s API listening on %s (TLS, agent mTLS=%t) db=%s", Version, addr, requireAgentMTLS, dbPath)
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			log.Printf("porter %s listening on %s db=%s", Version, addr, dbPath)
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Fatal(serveErr)
		}
	}()
	var gatewayServers []*http.Server
	if gwAddr != "" && gwAddr != "off" {
		gw := trackRequests.Wrap(telemetry.HTTPHandler(gateway.New(st), "porter.gateway"))
		if tlsMode == gatewayTLSACME {
			m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: acmeHostPolicy(st, acmeHosts), Cache: autocert.DirCache(env("PORTER_ACME_CACHE", "/var/lib/porter/certs"))}
			challengeServer := &http.Server{Addr: ":80", Handler: trackRequests.Wrap(m.HTTPHandler(nil)), ReadHeaderTimeout: 10 * time.Second, BaseContext: baseContext}
			gatewayServers = append(gatewayServers, challengeServer)
			go func() {
				log.Printf("porter ACME HTTP-01 on :80")
				if err := challengeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Printf("ACME HTTP-01 listener stopped: %v", err)
				}
			}()
			tlsServer := &http.Server{Addr: ":443", Handler: gw, TLSConfig: m.TLSConfig(), ReadHeaderTimeout: 10 * time.Second, BaseContext: baseContext}
			gatewayServers = append(gatewayServers, tlsServer)
			go func() {
				log.Printf("porter gateway ACME TLS on :443")
				if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
					log.Printf("gateway ACME TLS listener stopped: %v", err)
				}
			}()
		} else {
			gatewayServer := &http.Server{Addr: gwAddr, Handler: gw, ReadHeaderTimeout: 10 * time.Second, BaseContext: baseContext}
			gatewayServers = append(gatewayServers, gatewayServer)
			go func() {
				if tlsMode == gatewayTLSStatic {
					log.Printf("porter gateway listening on %s (TLS)", gwAddr)
					if err := gatewayServer.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
						log.Printf("gateway stopped: %v", err)
					}
				} else {
					log.Printf("porter gateway listening on %s (HTTP)", gwAddr)
					if err := gatewayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						log.Printf("gateway stopped: %v", err)
					}
				}
			}()
		}
	}
	reconcileCtx, reconcileCancel := context.WithCancel(context.Background())
	defer reconcileCancel()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-reconcileCtx.Done():
				return
			case <-t.C:
				if _, err := st.MarkHeartbeatExpiredNodesUnavailable(reconcileCtx, time.Now()); err != nil {
					log.Printf("node freshness reconciliation: %v", err)
				}
				if err := st.ReconcileAutoscale(reconcileCtx); err != nil {
					log.Printf("autoscale: %v", err)
				}
				_ = st.RequeueStaleCommands(reconcileCtx, time.Now().Add(-90*time.Second))
			}
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	trackRequests.StopAccepting()
	reconcileCancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	servers := append([]*http.Server{srv}, gatewayServers...)
	shutdownErrors := make(chan error, len(servers))
	var shutdownWait sync.WaitGroup
	for _, server := range servers {
		shutdownWait.Add(1)
		go func(server *http.Server) {
			defer shutdownWait.Done()
			if err := server.Shutdown(shutdownContext); err != nil {
				shutdownErrors <- fmt.Errorf("HTTP server %s shutdown: %w", server.Addr, err)
			}
		}(server)
	}
	shutdownWait.Wait()
	close(shutdownErrors)
	shutdownFailed := false
	for err := range shutdownErrors {
		shutdownFailed = true
		log.Printf("%v", err)
	}
	if shutdownFailed {
		cancelRequests()
		for _, server := range servers {
			if err := server.Close(); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTP server %s force close: %v", server.Addr, err)
			}
		}
	}
	trackRequests.Wait()
	shutdownCancel()
	traceContext, traceCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer traceCancel()
	if err := traceShutdown(traceContext); err != nil {
		log.Printf("OpenTelemetry trace shutdown: %v", err)
	}
	log.Printf("porter stopped")
}
