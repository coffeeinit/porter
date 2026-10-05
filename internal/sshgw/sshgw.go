// Package sshgw is the SSH gateway — the only Porter component meant to be
// internet-facing. There is NO sshd inside the guest: the gateway terminates
// SSH and will bridge the session through a future guest-vsock agent. Certs are
// short-lived user certs signed by the gateway CA.
package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// Execer is the bridge to a direct guest-vsock execution channel. argv carries
// the command (nil = shell); the SSH channel implements both streams.
type Execer interface {
	Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error
}

// Config holds the gateway's listen address and key paths.
type Config struct {
	ListenAddr string // e.g. ":2222"
	DataDir    string // where the CA + host key live
}

// Gateway terminates SSH for Porter-managed microVMs.
type Gateway struct {
	cfg     Config
	execer  Execer
	caKey   ed25519.PrivateKey
	hostKey ssh.Signer
	server  *ssh.ServerConfig
}

// New loads (or creates) the CA + host key and prepares the SSH server.
func New(cfg Config, execer Execer) (*Gateway, error) {
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	ca, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "ca_key"))
	if err != nil {
		return nil, fmt.Errorf("sshgw: ca key: %w", err)
	}
	host, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "host_key"))
	if err != nil {
		return nil, fmt.Errorf("sshgw: host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(host)
	if err != nil {
		return nil, fmt.Errorf("sshgw: host signer: %w", err)
	}

	g := &Gateway{cfg: cfg, execer: execer, caKey: ca, hostKey: signer}
	server := &ssh.ServerConfig{
		PublicKeyCallback: g.authorizeCert,
	}
	server.AddHostKey(signer)
	g.server = server
	return g, nil
}

// authorizeCert accepts only short-lived user certs signed by this gateway CA.
func (g *Gateway) authorizeCert(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	cert, ok := key.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("public key not a certificate")
	}
	if string(cert.SignatureKey.Marshal()) != string(g.hostKey.PublicKey().Marshal()) &&
		!g.signedByCA(cert) {
		return nil, fmt.Errorf("certificate not signed by gateway CA")
	}
	now := uint64(time.Now().Unix())
	if now > cert.ValidBefore || now < cert.ValidAfter {
		return nil, fmt.Errorf("certificate expired or not yet valid")
	}
	return &ssh.Permissions{Extensions: map[string]string{"vm": conn.User()}}, nil
}

func (g *Gateway) signedByCA(cert *ssh.Certificate) bool {
	if cert == nil || cert.SignatureKey == nil {
		return false
	}
	caPub, err := ssh.NewPublicKey(g.caKey.Public())
	if err != nil {
		return false
	}
	return string(cert.SignatureKey.Marshal()) == string(caPub.Marshal())
}

// SignCertificate signs a caller's public key into a 10-minute user cert for
// the given VM. The gateway verifies it on connect and then runs task.Exec.
func (g *Gateway) SignCertificate(pubKey []byte, vmID string) ([]byte, error) {
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	caPub, err := ssh.NewPublicKey(g.caKey.Public())
	if err != nil {
		return nil, err
	}
	cert := &ssh.Certificate{
		Key:             parsed,
		Serial:          uint64(time.Now().UnixNano()),
		CertType:        ssh.UserCert,
		KeyId:           vmID,
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(10 * time.Minute).Unix()),
		ValidPrincipals: []string{vmID},
		SignatureKey:    caPub,
	}
	if err := cert.SignCert(rand.Reader, g.hostKey); err != nil {
		return nil, fmt.Errorf("sign cert: %w", err)
	}
	return ssh.MarshalAuthorizedKey(cert), nil
}

// ListenAndServe runs the SSH server until ctx is cancelled.
func (g *Gateway) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", g.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("sshgw: listen %s: %w", g.cfg.ListenAddr, err)
	}
	defer ln.Close()
	log.Printf("sshgw: SSH gateway listening on %s", g.cfg.ListenAddr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go g.handleConn(conn)
	}
}

func (g *Gateway) handleConn(conn net.Conn) {
	sConn, chans, reqs, err := ssh.NewServerConn(conn, g.server)
	if err != nil {
		log.Printf("sshgw: handshake: %v", err)
		return
	}
	defer sConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				switch req.Type {
				case "shell":
					_ = req.Reply(true, nil)
					vmID := sConn.User()
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					if g.execer != nil {
						_ = g.execer.Exec(ctx, vmID, nil, ch, ch)
					}
					cancel()
				case "exec":
					_ = req.Reply(true, nil)
				case "pty-req":
					_ = req.Reply(true, nil)
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

// --- key helpers ---

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			return ed25519.PrivateKey(block.Bytes), nil
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blob := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PRIVATE KEY", Bytes: priv})
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}
