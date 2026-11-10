package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"porter/controlplane/internal/systemdcred"
	"porter/controlplane/internal/telemetry"
)

var Version = "v1.0.0-dev"

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

	addr := env("PORTER_ADDR", "127.0.0.1:8080")
	if err := validatePostgresOnlyGatewaySetting(os.Getenv("PORTER_GATEWAY_ADDR")); err != nil {
		log.Fatalf("invalid PostgreSQL-only configuration: %v", err)
	}
	apiTLSConfig, requireAgentMTLS, err := loadAPITLSConfig(
		os.Getenv("PORTER_API_TLS_CERT"),
		os.Getenv("PORTER_API_TLS_KEY"),
		os.Getenv("PORTER_AGENT_CLIENT_CA"),
	)
	if err != nil {
		log.Fatalf("invalid API TLS configuration: %v", err)
	}
	adminToken, err := systemdcred.ReadAdminToken(os.Getenv)
	if err != nil {
		log.Fatalf("invalid operator credential: %v", err)
	}
	security, err := resolveStartupSecurity(os.Getenv("PORTER_API_MODE"), adminToken)
	if err != nil {
		log.Fatalf("invalid startup security configuration: %v", err)
	}
	if err := validateAPIServerTransport(addr, apiTLSConfig != nil, security.APIMode); err != nil {
		log.Fatalf("unsafe API listener configuration: %v", err)
	}
	dsn, err := systemdcred.ReadPostgresDSN(os.Getenv)
	if err != nil {
		log.Fatalf("invalid PostgreSQL credential: %v", err)
	}
	apiHandler, closePostgresAPI, err := configureAPIRoutes(security.AdminToken, dsn)
	if err != nil {
		log.Fatalf("configure PostgreSQL APIs: %v", err)
	}
	defer closePostgresAPI()
	if requireAgentMTLS {
		apiHandler = requireAgentClientCertificate(apiHandler)
	}

	requestContext, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	trackRequests := newRequestTracker()
	baseContext := func(net.Listener) context.Context { return requestContext }
	traceShutdown, err := telemetry.SetupTracing(context.Background())
	if err != nil {
		log.Fatalf("configure OpenTelemetry tracing: %v", err)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           trackRequests.Wrap(telemetry.HTTPHandler(apiHandler, "porter.api")),
		TLSConfig:         apiTLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       baseContext,
	}
	go func() {
		var serveErr error
		if apiTLSConfig != nil {
			log.Printf("porter %s PostgreSQL API listening on %s (TLS)", Version, addr)
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			log.Printf("porter %s PostgreSQL API listening on %s (development HTTP)", Version, addr)
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Fatal(serveErr)
		}
	}()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	trackRequests.StopAccepting()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownContext); err != nil {
		log.Printf("HTTP server shutdown: %v", err)
		cancelRequests()
		if closeErr := srv.Close(); closeErr != nil && closeErr != http.ErrServerClosed {
			log.Printf("HTTP server force close: %v", closeErr)
		}
	}
	trackRequests.Wait()
	traceContext, traceCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer traceCancel()
	if err := traceShutdown(traceContext); err != nil {
		log.Printf("OpenTelemetry trace shutdown: %v", err)
	}
	log.Printf("porter stopped")
}
