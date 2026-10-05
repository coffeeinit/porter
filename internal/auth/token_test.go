package auth

import (
	"strings"
	"testing"
	"time"
)

func testKeys() KeyPair { return KeyPairFromSecret("test-secret-material", "porter-test") }

func TestKeyPairStableAcrossDerivation(t *testing.T) {
	a, b := testKeys(), testKeys()
	if a.KID != b.KID {
		t.Fatalf("derived KID must be stable: %q vs %q", a.KID, b.KID)
	}
}

func TestMintVerifyRoundTrip(t *testing.T) {
	k := testKeys()
	tok, err := k.Mint("alice", "porter-api", "project.read", "org-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(tok, ".") != 2 {
		t.Fatalf("token must have 3 segments: %q", tok)
	}
	c, err := k.Verify(tok, "porter-api")
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "alice" || c.Scope != "project.read" || c.Tenant != "org-1" {
		t.Fatalf("claims mismatch: %+v", c)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	k := testKeys()
	tok, err := k.Mint("alice", "porter-api", "s", "t", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	parts[1] = parts[1][:len(parts[1])-2] + "AA"
	if _, err := k.Verify(strings.Join(parts, "."), "porter-api"); err == nil {
		t.Fatal("tampered payload must fail")
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	k := testKeys()
	tok, err := k.Mint("alice", "porter-api", "s", "t", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Verify(tok, "other-api"); err == nil {
		t.Fatal("wrong audience must fail")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	k := testKeys()
	tok, err := k.Mint("alice", "porter-api", "s", "t", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Verify(tok, "porter-api"); err == nil {
		t.Fatal("expired token must fail")
	}
}

func TestVerifyRejectsForeignKey(t *testing.T) {
	a := KeyPairFromSecret("one", "porter-test")
	b := KeyPairFromSecret("two", "porter-test")
	tok, err := a.Mint("alice", "porter-api", "s", "t", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(tok, "porter-api"); err == nil {
		t.Fatal("foreign key must fail")
	}
}

func TestPublicJWKShape(t *testing.T) {
	jwk := testKeys().PublicJWK()
	for _, f := range []string{"kty", "crv", "alg", "use", "kid", "x"} {
		if jwk[f] == "" {
			t.Fatalf("JWK missing %q", f)
		}
	}
	if jwk["kty"] != "OKP" || jwk["crv"] != "Ed25519" || jwk["alg"] != "EdDSA" {
		t.Fatalf("JWK must be Ed25519 EdDSA: %+v", jwk)
	}
}

func TestNewOpaqueTokenFormat(t *testing.T) {
	raw, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "prt_") || len(hash) != 64 {
		t.Fatalf("bad opaque token shape: %q %q", raw, hash)
	}
}
