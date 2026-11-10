package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"porter/controlplane/internal/api"
)

func TestLoadAPITLSConfigRequiresCompleteAndValidFiles(t *testing.T) {
	if config, required, err := loadAPITLSConfig("", "", ""); err != nil || config != nil || required {
		t.Fatalf("empty config = (%v, %t, %v), want (nil, false, nil)", config, required, err)
	}
	if _, _, err := loadAPITLSConfig("server.crt", "", ""); err == nil {
		t.Fatal("certificate without key was accepted")
	}
	if _, _, err := loadAPITLSConfig("", "", "client-ca.pem"); err == nil {
		t.Fatal("client CA without API server certificate was accepted")
	}

	files := writeAPITestCertificates(t)
	config, required, err := loadAPITLSConfig(files.serverCert, files.serverKey, "")
	if err != nil || required || config == nil || config.ClientAuth != tls.NoClientCert || config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS-only config = (%v, %t, %v), want TLS without client auth", config, required, err)
	}
	config, required, err = loadAPITLSConfig(files.serverCert, files.serverKey, files.caCert)
	if err != nil || !required || config == nil || config.ClientAuth != tls.VerifyClientCertIfGiven || config.ClientCAs == nil {
		t.Fatalf("mTLS config = (%v, %t, %v), want verified optional handshake plus route enforcement", config, required, err)
	}

	badCA := filepath.Join(t.TempDir(), "bad-ca.pem")
	if err := os.WriteFile(badCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAPITLSConfig(files.serverCert, files.serverKey, badCA); err == nil {
		t.Fatal("invalid client CA was accepted")
	}
}

func TestAPIMTLSRequiresVerifiedNodeCertificateOnlyOnAgentRoutes(t *testing.T) {
	files := writeAPITestCertificates(t)
	tlsConfig, requireAgentMTLS, err := loadAPITLSConfig(files.serverCert, files.serverKey, files.caCert)
	if err != nil {
		t.Fatal(err)
	}
	if !requireAgentMTLS {
		t.Fatal("client CA did not enable agent mTLS enforcement")
	}

	server := httptest.NewUnstartedServer(api.RequireAgentMTLS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()

	noCertClient := testTLSClient(t, files.caCert, "", "")
	assertAPIStatus(t, noCertClient, server.URL+"/v1/fleet/heartbeat", "node-1", http.StatusUnauthorized)
	assertAPIStatus(t, noCertClient, server.URL+"/v1/fleet/enroll", "", http.StatusNoContent)
	assertAPIStatus(t, noCertClient, server.URL+"/healthz", "", http.StatusNoContent)

	validClient := testTLSClient(t, files.caCert, files.node1Cert, files.node1Key)
	assertAPIStatus(t, validClient, server.URL+"/v1/fleet/heartbeat", "node-1", http.StatusNoContent)

	wrongNodeClient := testTLSClient(t, files.caCert, files.node2Cert, files.node2Key)
	assertAPIStatus(t, wrongNodeClient, server.URL+"/v1/fleet/heartbeat", "node-1", http.StatusUnauthorized)

	wrongCAClient := testTLSClient(t, files.caCert, files.node3Cert, files.node3Key)
	assertAPIAccessDenied(t, wrongCAClient, server.URL+"/v1/fleet/heartbeat", "node-1")
}

type apiTestCertificates struct {
	caCert     string
	serverCert string
	serverKey  string
	node1Cert  string
	node1Key   string
	node2Cert  string
	node2Key   string
	node3Cert  string
	node3Key   string
}

func writeAPITestCertificates(t *testing.T) apiTestCertificates {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "Porter test client CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ca := mustParseTestCertificate(t, caDER)
	caCertFile := writeTestPEM(t, dir, "client-ca.pem", "CERTIFICATE", caDER, 0o644)

	serverCert, serverKey := issueAPITestLeaf(t, dir, "api", ca, caPrivate, "api", true)
	node1Cert, node1Key := issueAPITestLeaf(t, dir, "node-1", ca, caPrivate, "node-1", false)
	node2Cert, node2Key := issueAPITestLeaf(t, dir, "node-2", ca, caPrivate, "node-2", false)
	rogueCAPublic, rogueCAPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rogueCATemplate := *caTemplate
	rogueCATemplate.SerialNumber = big.NewInt(now.UnixNano() + 1)
	rogueCATemplate.Subject = pkix.Name{CommonName: "Untrusted test CA"}
	rogueCADER, err := x509.CreateCertificate(rand.Reader, &rogueCATemplate, &rogueCATemplate, rogueCAPublic, rogueCAPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rogueCA := mustParseTestCertificate(t, rogueCADER)
	node3Cert, node3Key := issueAPITestLeaf(t, dir, "node-3", rogueCA, rogueCAPrivate, "node-1", false)
	return apiTestCertificates{
		caCert: caCertFile, serverCert: serverCert, serverKey: serverKey,
		node1Cert: node1Cert, node1Key: node1Key, node2Cert: node2Cert, node2Key: node2Key,
		node3Cert: node3Cert, node3Key: node3Key,
	}
}

func issueAPITestLeaf(t *testing.T, dir, filename string, ca *x509.Certificate, caKey ed25519.PrivateKey, nodeID string, server bool) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	usage := x509.ExtKeyUsageClientAuth
	if server {
		usage = x509.ExtKeyUsageServerAuth
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: nodeID},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
	}
	if server {
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		identity, err := url.Parse("spiffe://porter/node/" + nodeID)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{identity}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile := writeTestPEM(t, dir, filename+".pem", "CERTIFICATE", der, 0o644)
	keyFile := writeTestPEM(t, dir, filename+"-key.pem", "PRIVATE KEY", privateDER, 0o600)
	return certFile, keyFile
}

func mustParseTestCertificate(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func writeTestPEM(t *testing.T, dir, filename, blockType string, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data}), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func testTLSClient(t *testing.T, caFile, certFile, keyFile string) *http.Client {
	t.Helper()
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to load test CA")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			t.Fatal("test client certificate and key must be paired")
		}
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	transport := &http.Transport{TLSClientConfig: config}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func assertAPIStatus(t *testing.T, client *http.Client, target, nodeID string, expected int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != "" {
		request.Header.Set("X-Porter-Node-ID", nodeID)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request to %s: %v", target, err)
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		t.Fatalf("status for %s = %d, want %d", target, response.StatusCode, expected)
	}
}

func assertAPIAccessDenied(t *testing.T, client *http.Client, target, nodeID string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Porter-Node-ID", nodeID)
	response, err := client.Do(request)
	if err != nil {
		return // TLS rejected the untrusted certificate during the handshake.
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("request with a certificate from an untrusted CA got status %d, want a TLS failure or 401", response.StatusCode)
	}
}
