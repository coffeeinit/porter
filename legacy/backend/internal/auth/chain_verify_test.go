package auth

// Hermetic verification of the login → credential → scope chain (Agent C).
// No PG, no HTTP: password primitives, opaque API-key tokens, JWT
// (EdDSA+JWKS) claims and X-Tenant-ID scope resolution as composed by
// POST /auth/login + resolveBearer + AuthContext.

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashPassword("correct-horse-12", salt)
	if !VerifyPassword("correct-horse-12", salt, hash) {
		t.Fatal("valid password must verify")
	}
}

func TestPasswordWrongRejected(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	hash := HashPassword("correct-horse-12", salt)
	if VerifyPassword("wrong-password", salt, hash) {
		t.Fatal("wrong password must not verify")
	}
}

func TestPasswordSaltIsolation(t *testing.T) {
	s1, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Fatal("salts must differ")
	}
	hash := HashPassword("same-password-12", s1)
	if VerifyPassword("same-password-12", s2, hash) {
		t.Fatal("hash must not verify under a different salt")
	}
}

func TestPasswordEmptyRejected(t *testing.T) {
	if VerifyPassword("", "salt", "hash") {
		t.Fatal("empty password must not verify")
	}
	if VerifyPassword("x", "", "hash") {
		t.Fatal("empty salt must not verify")
	}
	if VerifyPassword("x", "salt", "") {
		t.Fatal("empty expected hash must not verify")
	}
	if err := ValidateBootstrapPassword("short"); err == nil {
		t.Fatal("short bootstrap password must be rejected")
	}
	if err := ValidateBootstrapPassword("long-enough-bootstrap-pw"); err != nil {
		t.Fatalf("adequate bootstrap password must pass: %v", err)
	}
}

func TestOpaqueTokensUniqueAndHashed(t *testing.T) {
	raw1, hash1, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	raw2, hash2, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw1 == raw2 {
		t.Fatal("opaque tokens must be unique")
	}
	if hash1 == hash2 {
		t.Fatal("opaque token hashes must be unique")
	}
	for _, raw := range []string{raw1, raw2} {
		if !strings.HasPrefix(raw, "prt_") {
			t.Fatalf("opaque token must carry prt_ prefix: %q", raw)
		}
	}
	if len(hash1) != 64 || len(hash2) != 64 {
		t.Fatal("opaque token hash must be 64-char sha256 hex")
	}
}

func TestLoginChainJWTTenantScope(t *testing.T) {
	// Simulates what POST /auth/login + handleMintToken + resolveBearer do:
	// password verifies, a JWT is minted with the tenant claim, the claim
	// verifies and resolves to the tenant scope (never to "*").
	k := KeyPairFromSecret("chain-test-secret", "porter-test")
	tok, err := k.Mint("alice", "porter-api", "session", "org-1", 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := k.Verify(tok, "porter-api")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "alice" || claims.Tenant != "org-1" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	scopeType, scopeID := ResolveScope("", claims.Tenant)
	if scopeType != "org" || scopeID != "org-1" {
		t.Fatalf("tenant claim must resolve to org scope, got %q/%q", scopeType, scopeID)
	}
	if scopeType == "platform" {
		t.Fatal("non-empty tenant must never resolve to platform scope")
	}
}

func TestCentralTenantClaimStaysPlatform(t *testing.T) {
	// A "*" tenant claim (central caller) must resolve to platform scope.
	scopeType, scopeID := ResolveScope("", CentralTenant)
	if scopeType != "platform" || scopeID != "" {
		t.Fatalf("'*' must resolve to platform scope, got %q/%q", scopeType, scopeID)
	}
}
