package main

import (
	"reflect"
	"testing"
)

func TestGatewayTLSConfig(t *testing.T) {
	tests := []struct {
		name      string
		cert      string
		key       string
		acme      string
		wantMode  gatewayTLSMode
		wantHosts []string
		wantError bool
	}{
		{name: "plain HTTP", wantMode: gatewayTLSHTTP},
		{name: "static certificate pair", cert: "/etc/porter/cert.pem", key: "/etc/porter/key.pem", wantMode: gatewayTLSStatic},
		{name: "HTTP-01 host list is trimmed and deduplicated", acme: " app.example.com, api.example.com,APP.example.com ", wantMode: gatewayTLSACME, wantHosts: []string{"app.example.com", "api.example.com"}},
		{name: "certificate without key fails", cert: "/etc/porter/cert.pem", wantError: true},
		{name: "key without certificate fails", key: "/etc/porter/key.pem", wantError: true},
		{name: "ACME and static TLS conflict", cert: "/etc/porter/cert.pem", key: "/etc/porter/key.pem", acme: "app.example.com", wantError: true},
		{name: "empty ACME hostname fails", acme: "app.example.com,,api.example.com", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, hosts, err := gatewayTLSConfig(tt.cert, tt.key, tt.acme)
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
