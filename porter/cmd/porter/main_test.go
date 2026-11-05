package main

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"porter/controlplane/internal/store"
)

func TestGatewayTLSConfig(t *testing.T) {
	tests := []struct {
		name        string
		cert        string
		key         string
		acme        string
		acmeEnabled string
		wantMode    gatewayTLSMode
		wantHosts   []string
		wantError   bool
	}{
		{name: "plain HTTP", wantMode: gatewayTLSHTTP},
		{name: "static certificate pair", cert: "/etc/porter/cert.pem", key: "/etc/porter/key.pem", wantMode: gatewayTLSStatic},
		{name: "HTTP-01 host list is trimmed and deduplicated", acme: " app.example.com, api.example.com,APP.example.com ", wantMode: gatewayTLSACME, wantHosts: []string{"app.example.com", "api.example.com"}},
		{name: "enabled ACME uses dynamic active-domain policy", acmeEnabled: "true", wantMode: gatewayTLSACME},
		{name: "ACME enable is case insensitive", acmeEnabled: " TRUE ", wantMode: gatewayTLSACME},
		{name: "explicit ACME false keeps HTTP mode", acmeEnabled: "false", wantMode: gatewayTLSHTTP},
		{name: "invalid ACME enable value fails", acmeEnabled: "yes", wantError: true},
		{name: "certificate without key fails", cert: "/etc/porter/cert.pem", wantError: true},
		{name: "key without certificate fails", key: "/etc/porter/key.pem", wantError: true},
		{name: "ACME list and static TLS conflict", cert: "/etc/porter/cert.pem", key: "/etc/porter/key.pem", acme: "app.example.com", wantError: true},
		{name: "ACME enable and static TLS conflict", cert: "/etc/porter/cert.pem", key: "/etc/porter/key.pem", acmeEnabled: "true", wantError: true},
		{name: "empty ACME hostname fails", acme: "app.example.com,,api.example.com", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, hosts, err := gatewayTLSConfig(tt.cert, tt.key, tt.acme, tt.acmeEnabled)
			if (err != nil) != tt.wantError {
				t.Fatalf("gatewayTLSConfig() error = %v, wantError %v", err, tt.wantError)
			}
			if err != nil {
				return
			}
			if mode != tt.wantMode {
				t.Errorf("gatewayTLSConfig() mode = %q, want %q", mode, tt.wantMode)
			}
			if !reflect.DeepEqual(hosts, tt.wantHosts) {
				t.Errorf("gatewayTLSConfig() hosts = %#v, want %#v", hosts, tt.wantHosts)
			}
		})
	}
}

func TestACMEHostPolicyRequiresActiveRoutedDomain(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	project, err := st.CreateProject(ctx, "acme-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(ctx, project.ID, "acme-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	active, err := st.CreateManagedDomain(ctx, project.ID, app.ID, "Active.Example.COM.", "production")
	if err != nil {
		t.Fatalf("create active domain: %v", err)
	}
	pending, err := st.CreateDomain(ctx, project.ID, app.ID, "pending.example.com", "custom")
	if err != nil {
		t.Fatalf("create pending domain: %v", err)
	}
	other, err := st.CreateManagedDomain(ctx, project.ID, app.ID, "other.example.com", "production")
	if err != nil {
		t.Fatalf("create other active domain: %v", err)
	}

	dynamicPolicy := acmeHostPolicy(st, nil)
	if err := dynamicPolicy(ctx, "ACTIVE.EXAMPLE.COM."); err != nil {
		t.Fatalf("active routed domain rejected: %v", err)
	}
	if err := dynamicPolicy(ctx, pending.Hostname); err == nil {
		t.Fatal("pending custom domain accepted for ACME")
	}
	if err := dynamicPolicy(ctx, "unregistered.example.com"); err == nil {
		t.Fatal("unregistered host accepted for ACME")
	}
	const futureHost = "created-later.example.com"
	if err := dynamicPolicy(ctx, futureHost); err == nil {
		t.Fatal("domain without a store route accepted for ACME")
	}
	later, err := st.CreateManagedDomain(ctx, project.ID, app.ID, futureHost, "production")
	if err != nil {
		t.Fatalf("create domain after policy construction: %v", err)
	}
	if err := dynamicPolicy(ctx, later.Hostname); err != nil {
		t.Fatalf("newly routed domain was not dynamically authorized: %v", err)
	}

	restrictedPolicy := acmeHostPolicy(st, []string{"active.example.com"})
	if err := restrictedPolicy(ctx, active.Hostname); err != nil {
		t.Fatalf("allowlisted active domain rejected: %v", err)
	}
	if err := restrictedPolicy(ctx, other.Hostname); err == nil {
		t.Fatal("active domain outside the configured allowlist accepted for ACME")
	}
}

func TestACMEHostPolicyFailsClosedOnStoreError(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	project, err := st.CreateProject(ctx, "acme-error-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(ctx, project.ID, "acme-error-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	domain, err := st.CreateManagedDomain(ctx, project.ID, app.ID, "eligible.example.com", "production")
	if err != nil {
		t.Fatalf("create active domain: %v", err)
	}
	policy := acmeHostPolicy(st, nil)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := policy(ctx, domain.Hostname); err == nil {
		t.Fatal("ACME policy accepted a domain after its store became unavailable")
	}
}
