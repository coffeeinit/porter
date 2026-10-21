package sshgw

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ListenAddr: "127.0.0.1:0",
		DataDir:    t.TempDir(),
	}
}

func TestNewCreatesKeysAndReloads(t *testing.T) {
	cfg := testConfig(t)
	g1, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := New(cfg, nil) // second load must reuse the same keys
	if err != nil {
		t.Fatal(err)
	}
	if string(g1.caKey.Public().(ed25519.PublicKey)) != string(g2.caKey.Public().(ed25519.PublicKey)) {
		t.Fatal("CA key not stable across reloads")
	}
}

func TestSignCertificateProducesVMCert(t *testing.T) {
	cfg := testConfig(t)
	g, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	certBytes, err := g.SignCertificate(ssh.MarshalAuthorizedKey(sshPub), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		t.Fatalf("signed blob is not an authorized key: %v", err)
	}
	cert, ok := parsed.(*ssh.Certificate)
	if !ok {
		t.Fatal("expected a certificate")
	}
	if cert.KeyId != "vm-1" {
		t.Fatalf("unexpected key id %q", cert.KeyId)
	}
	if len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != "vm-1" {
		t.Fatalf("unexpected principals %v", cert.ValidPrincipals)
	}
}

type fakeConn struct{ user string }

func (c *fakeConn) User() string           { return c.user }
func (c *fakeConn) SessionID() []byte      { return nil }
func (c *fakeConn) ClientVersion() []byte  { return []byte("SSH-2.0-test") }
func (c *fakeConn) ServerVersion() []byte  { return []byte("SSH-2.0-porter") }
func (c *fakeConn) RemoteAddr() net.Addr   { return nil }
func (c *fakeConn) LocalAddr() net.Addr    { return nil }
func (c *fakeConn) Timeout() time.Duration { return 0 }

func TestAuthorizeCertAcceptsSignedRejectsPlain(t *testing.T) {
	cfg := testConfig(t)
	g, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	// signed cert is accepted
	certBytes, err := g.SignCertificate(ssh.MarshalAuthorizedKey(sshPub), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.authorizeCert(&fakeConn{user: "vm-1"}, parsed); err != nil {
		t.Fatalf("signed cert rejected: %v", err)
	}

	// plain (unsigned) public key is rejected
	if _, err := g.authorizeCert(&fakeConn{user: "vm-1"}, sshPub); err == nil {
		t.Fatal("expected unsigned key to be rejected")
	}
}
