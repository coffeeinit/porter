package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"porter/protocol"
)

func TestAgentClientUsesMutualTLSAndServerCA(t *testing.T) {
	clientCA, clientCAKey, clientCAPEM := makeClientTestCA(t)
	clientCertFile, clientKeyFile := writeAgentTestClientCert(t, clientCA, clientCAKey)
	clientCAPool := x509.NewCertPool()
	if !clientCAPool.AppendCertsFromPEM(clientCAPEM) {
		t.Fatal("failed to load client CA")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agent/heartbeat" {
			t.Errorf("path = %q, want heartbeat", r.URL.Path)
		}
		if r.Header.Get("X-Porter-Node-ID") != "node-1" || r.Header.Get("Authorization") != "Bearer node-token" {
			t.Errorf("agent identity headers missing: node=%q auth=%q", r.Header.Get("X-Porter-Node-ID"), r.Header.Get("Authorization"))
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			t.Error("request did not contain a verified client certificate")
		} else if len(r.TLS.PeerCertificates[0].URIs) != 1 || r.TLS.PeerCertificates[0].URIs[0].String() != "spiffe://porter/node/node-1" {
			t.Errorf("client certificate URI identity = %v, want node-1", r.TLS.PeerCertificates[0].URIs)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAPool,
	}
	server.StartTLS()
	defer server.Close()

	bundleFile := filepath.Join(t.TempDir(), "server-and-client-ca.pem")
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), clientCAPEM...)
	if err := os.WriteFile(bundleFile, bundle, 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewWithTLS(server.URL, "node-1", "node-token", TLSOptions{
		ServerCAFile: bundleFile, ClientCertFile: clientCertFile, ClientKeyFile: clientKeyFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Heartbeat(context.Background(), protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()}); err != nil {
		t.Fatalf("mutual-TLS heartbeat failed: %v", err)
	}

	withoutCertificate, err := NewWithTLS(server.URL, "node-1", "node-token", TLSOptions{ServerCAFile: bundleFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutCertificate.Heartbeat(context.Background(), protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()}); err == nil {
		t.Fatal("heartbeat without a client certificate succeeded")
	}
}

func TestNewHTTPClientRejectsIncompleteClientIdentity(t *testing.T) {
	if _, err := NewHTTPClient(TLSOptions{ClientCertFile: "agent.pem"}); err == nil {
		t.Fatal("client certificate without key was accepted")
	}
	if _, err := NewHTTPClient(TLSOptions{ClientKeyFile: "agent-key.pem"}); err == nil {
		t.Fatal("client key without certificate was accepted")
	}
}

func TestTLSOptionsRequireHTTPSControlPlane(t *testing.T) {
	options := TLSOptions{ServerCAFile: filepath.Join(t.TempDir(), "unused-ca.pem")}
	if _, err := NewWithTLS("http://127.0.0.1:8080", "node-1", "token", options); err == nil {
		t.Fatal("TLS options were silently accepted with loopback HTTP")
	}
	if _, err := JoinWithTLS(context.Background(), "http://localhost:8080", protocol.EnrollRequest{}, options); err == nil {
		t.Fatal("join TLS options were silently accepted with loopback HTTP")
	}
	if _, err := NewWithTLS("http://127.0.0.1:8080", "node-1", "token", TLSOptions{}); err != nil {
		t.Fatalf("loopback HTTP without TLS options should remain available for development: %v", err)
	}
}

func makeClientTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "Porter agent test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeAgentTestClientCert(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := url.Parse("spiffe://porter/node/node-1")
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "node-1"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:                  []*url.URL{identity},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "agent.pem")
	keyFile := filepath.Join(dir, "agent-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
