// Short-lived scoped console tokens (OCM-10 remainder): the control plane
// mints HS256 JWTs per workload/scope, the in-VM authproxy verifies them,
// and rotation happens on workload stop. Manual HS256 (stdlib only): header
// + payload + HMAC-SHA256, base64url, no padding. Tokens ride headers,
// never URLs.
package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Console scopes.
const (
	ScopeTerminal = "terminal"
	ScopeGateway  = "gateway"
	ScopePort     = "port"
)

// ConsoleClaims is the token payload.
type ConsoleClaims struct {
	VMID      string `json:"vm"`
	Scope     string `json:"scope"`
	ExpiresAt int64  `json:"exp"`
}

// MintConsoleToken issues a scoped token with ttl (5m convention, max 15m).
func MintConsoleToken(signingKey []byte, vmID, scope string, ttl time.Duration) (string, error) {
	if len(signingKey) == 0 {
		return "", fmt.Errorf("agent: console signing key required")
	}
	switch scope {
	case ScopeTerminal, ScopeGateway, ScopePort:
	default:
		return "", fmt.Errorf("agent: unknown console scope %q", scope)
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		return "", fmt.Errorf("agent: console ttl must be 0 < ttl <= 15m")
	}
	if vmID == "" {
		return "", fmt.Errorf("agent: console token needs a vm")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(ConsoleClaims{VMID: vmID, Scope: scope, ExpiresAt: time.Now().Add(ttl).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return header + "." + payload + "." + sig, nil
}

// VerifyConsoleToken checks signature, vm binding and expiry, returning the
// granted scope.
func VerifyConsoleToken(signingKey []byte, vmID, token string, now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("agent: malformed console token")
	}
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if hmac.Equal([]byte(parts[2]), []byte(want)) != true {
		return "", fmt.Errorf("agent: bad console signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("agent: bad console claims: %w", err)
	}
	var c ConsoleClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("agent: bad console claims: %w", err)
	}
	if c.VMID != vmID {
		return "", fmt.Errorf("agent: console token bound to another vm")
	}
	if now.Unix() > c.ExpiresAt {
		return "", fmt.Errorf("agent: console token expired")
	}
	return c.Scope, nil
}
