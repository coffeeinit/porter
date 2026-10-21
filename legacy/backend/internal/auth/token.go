package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file implements JWT (EdDSA/Ed25519) minting + verification and opaque
// token helpers (task T3). No external JWT dependency: the token shape is the
// Atlas-compatible subset {iss,sub,aud,scope,tenant,iat,exp} with a kid header.

// Required JWT claims for a usable Porter token.
const (
	ClaimIssuer   = "iss"
	ClaimSubject  = "sub"
	ClaimAudience = "aud"
	ClaimScope    = "scope"
	ClaimTenant   = "tenant"
	ClaimIssuedAt = "iat"
	ClaimExpiry   = "exp"
)

// Claims is the verified claim set of one token.
type Claims struct {
	Issuer   string
	Subject  string
	Audience string
	Scope    string
	Tenant   string
	IssuedAt time.Time
	Expiry   time.Time
	Raw      map[string]interface{}
}

// KeyPair is an Ed25519 signing identity with a stable key id.
type KeyPair struct {
	Seed []byte // 32-byte seed; derived deterministically when configured
	Key  ed25519.PrivateKey
	KID  string
}

// KeyPairFromSecret derives a stable signing identity from server key material
// (e.g. the configured secret key) so tokens survive restarts without new config.
func KeyPairFromSecret(secret, issuer string) KeyPair {
	sum := sha256.Sum256([]byte("porter-jwt-v1:" + secret))
	priv := ed25519.NewKeyFromSeed(sum[:])
	pub := priv.Public().(ed25519.PublicKey)
	kid := base64.RawURLEncoding.EncodeToString(pub[:8])
	return KeyPair{Seed: sum[:], Key: priv, KID: issuer + ":" + kid}
}

// Mint issues a token for subject with the given audience/scope/tenant/ttl.
func (k KeyPair) Mint(subject, audience, scope, tenant string, ttl time.Duration) (string, error) {
	now := time.Now().UTC().Truncate(time.Second)
	header := map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": k.KID}
	payload := map[string]interface{}{
		ClaimIssuer:   issuerOf(k.KID),
		ClaimSubject:  subject,
		ClaimAudience: audience,
		ClaimScope:    scope,
		ClaimTenant:   tenant,
		ClaimIssuedAt: now.Unix(),
		ClaimExpiry:   now.Add(ttl).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h, p := base64.RawURLEncoding.EncodeToString(hb), base64.RawURLEncoding.EncodeToString(pb)
	sig := ed25519.Sign(k.Key, []byte(h+"."+p))
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks signature, algorithm, required claims, audience and expiry.
func (k KeyPair) Verify(token, audience string) (Claims, error) {
	var zero Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return zero, fmt.Errorf("auth: malformed token")
	}
	var header map[string]string
	if err := decodeSegment(parts[0], &header); err != nil {
		return zero, fmt.Errorf("auth: bad header: %w", err)
	}
	if header["alg"] != "EdDSA" {
		return zero, fmt.Errorf("auth: unexpected alg %q", header["alg"])
	}
	if header["kid"] != k.KID {
		return zero, fmt.Errorf("auth: unknown kid")
	}
	var payload map[string]interface{}
	if err := decodeSegment(parts[1], &payload); err != nil {
		return zero, fmt.Errorf("auth: bad payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return zero, fmt.Errorf("auth: bad signature: %w", err)
	}
	if !ed25519.Verify(k.Key.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
		return zero, fmt.Errorf("auth: invalid signature")
	}
	str := func(key string) string {
		s, _ := payload[key].(string)
		return s
	}
	num := func(key string) int64 {
		switch v := payload[key].(type) {
		case float64:
			return int64(v)
		case json.Number:
			n, _ := v.Int64()
			return n
		}
		return 0
	}
	for _, key := range []string{ClaimIssuer, ClaimSubject, ClaimAudience, ClaimScope, ClaimTenant} {
		if str(key) == "" {
			return zero, fmt.Errorf("auth: missing claim %q", key)
		}
	}
	if aud := str(ClaimAudience); aud != audience {
		return zero, fmt.Errorf("auth: audience mismatch")
	}
	exp := num(ClaimExpiry)
	if exp == 0 || time.Now().UTC().Unix() > exp {
		return zero, fmt.Errorf("auth: token expired")
	}
	return Claims{
		Issuer: str(ClaimIssuer), Subject: str(ClaimSubject),
		Audience: str(ClaimAudience), Scope: str(ClaimScope), Tenant: str(ClaimTenant),
		IssuedAt: time.Unix(num(ClaimIssuedAt), 0).UTC(),
		Expiry:   time.Unix(exp, 0).UTC(), Raw: payload,
	}, nil
}

// PublicJWK exports the verify key as a JWK (OKP/Ed25519) for GET /auth/jwks.
func (k KeyPair) PublicJWK() map[string]string {
	pub := k.Key.Public().(ed25519.PublicKey)
	return map[string]string{
		"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig",
		"kid": k.KID, "x": base64.RawURLEncoding.EncodeToString(pub),
	}
}

func issuerOf(kid string) string {
	if i := strings.Index(kid, ":"); i > 0 {
		return kid[:i]
	}
	return kid
}

func decodeSegment(seg string, v interface{}) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// NewOpaqueToken generates a random opaque token (prt_ prefix) for API keys
// and sessions. Only the hash is persisted; the raw value is shown once.
func NewOpaqueToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = "prt_" + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}
