package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"porter/controlplane/internal/api"

	"porter/controlplane/internal/gateway"
	"porter/controlplane/internal/store"
)

var Version = "v1.0.0-dev"

type gatewayTLSMode string

const (
	gatewayTLSHTTP   gatewayTLSMode = "http"
	gatewayTLSStatic gatewayTLSMode = "static"
	gatewayTLSACME   gatewayTLSMode = "acme"
)

func gatewayTLSConfig(certFile, keyFile, acmeDomains string) (gatewayTLSMode, []string, error) {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	acmeDomains = strings.TrimSpace(acmeDomains)
	hasCert, hasKey := certFile != "", keyFile != ""
	if hasCert != hasKey {
		return "", nil, fmt.Errorf("PORTER_TLS_CERT and PORTER_TLS_KEY must be set together")
	}
	if acmeDomains != "" {
		if hasCert {
			return "", nil, fmt.Errorf("configure either PORTER_ACME_DOMAINS or PORTER_TLS_CERT/PORTER_TLS_KEY, not both")
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

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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
	tlsMode, acmeHosts, tlsErr := gatewayTLSConfig(certFile, keyFile, acmeDomains)
	if gwAddr != "" && gwAddr != "off" && tlsErr != nil {
		log.Fatalf("invalid gateway TLS configuration: %v", tlsErr)
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
	baseDomain := env("PORTER_BASE_DOMAIN", "porter.local")
	srv := &http.Server{Addr: addr, Handler: api.New(st, admin, baseDomain).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("porter %s listening on %s db=%s", Version, addr, dbPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	if gwAddr != "" && gwAddr != "off" {
		gw := gateway.New(st)
		if tlsMode == gatewayTLSACME {
			m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(acmeHosts...), Cache: autocert.DirCache(env("PORTER_ACME_CACHE", "/var/lib/porter/certs"))}
			go func() {
				log.Printf("porter ACME HTTP-01 on :80")
				if err := http.ListenAndServe(":80", m.HTTPHandler(nil)); err != nil {
					log.Printf("ACME HTTP-01 listener stopped: %v", err)
				}
			}()
			go func() {
				log.Printf("porter gateway ACME TLS on :443")
				srv := &http.Server{Addr: ":443", Handler: gw, TLSConfig: m.TLSConfig()}
				if err := srv.ListenAndServeTLS("", ""); err != nil {
					log.Printf("gateway ACME TLS listener stopped: %v", err)
				}
			}()
		} else {
			go func() {
				if tlsMode == gatewayTLSStatic {
					log.Printf("porter gateway listening on %s (TLS)", gwAddr)
					if err := http.ListenAndServeTLS(gwAddr, certFile, keyFile, gw); err != nil {
						log.Printf("gateway stopped: %v", err)
					}
					return
				}
				log.Printf("porter gateway listening on %s (HTTP)", gwAddr)
				if err := http.ListenAndServe(gwAddr, gw); err != nil {
					log.Printf("gateway stopped: %v", err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Printf("porter stopped")
}
