package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// loadAPITLSConfig returns a TLS server config and whether agent routes must
// require client certificates. Setting clientCAFile enables mTLS enforcement;
// API serving then requires a certificate/key pair and a valid client CA.
func loadAPITLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, bool, error) {
	if (certFile == "") != (keyFile == "") {
		return nil, false, fmt.Errorf("PORTER_API_TLS_CERT and PORTER_API_TLS_KEY must be set together")
	}
	if clientCAFile != "" && certFile == "" {
		return nil, false, fmt.Errorf("PORTER_AGENT_CLIENT_CA requires PORTER_API_TLS_CERT and PORTER_API_TLS_KEY")
	}
	if certFile == "" {
		return nil, false, nil
	}

	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, false, fmt.Errorf("load API TLS certificate: %w", err)
	}
	config := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	if clientCAFile == "" {
		return config, false, nil
	}

	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, false, fmt.Errorf("read agent client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, false, fmt.Errorf("agent client CA file contains no valid certificates")
	}
	config.ClientCAs = clientCAs
	config.ClientAuth = tls.VerifyClientCertIfGiven
	return config, true, nil
}
