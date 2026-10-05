// Edge verification (OCM-26): the gateway edge verifies workload JWTs
// itself (no control-plane round trip on the hot path), reads HMAC-signed
// route payloads, and decides bot traffic (prerendered HTML for crawlers,
// app shell for users). Signing keys are KMS-held; this file owns the
// pure verify/decide logic.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SignRoute signs a hostname->backend mapping for cache transport.
func SignRoute(key []byte, hostname, backend string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(hostname + "\x00" + backend))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyRoute checks a cached mapping signature. Empty material fails
// closed (fail-closed cache: miss, then re-resolve from Postgres truth).
func VerifyRoute(key []byte, hostname, backend, sig string) bool {
	if len(key) == 0 || hostname == "" || sig == "" {
		return false
	}
	want := SignRoute(key, hostname, backend)
	return hmac.Equal([]byte(want), []byte(sig))
}

// BotAgents are crawler substrings that receive prerendered HTML.
var BotAgents = []string{"googlebot", "bingbot", "slurp", "duckduckbot", "baiduspider", "twitterbot", "facebookexternalhit"}

// EdgeDecision routes one edge request: bots get prerender, users get the
// app shell, and unsigned/unknown hosts fall through to the control plane.
type EdgeDecision string

const (
	EdgePrerender EdgeDecision = "prerender"
	EdgeApp       EdgeDecision = "app"
	EdgeResolve   EdgeDecision = "resolve" // fall through to PG truth
)

// Decide classifies by user-agent and route-signature state.
func Decide(userAgent string, signed bool) EdgeDecision {
	if !signed {
		return EdgeResolve
	}
	ua := strings.ToLower(userAgent)
	for _, bot := range BotAgents {
		if strings.Contains(ua, bot) {
			return EdgePrerender
		}
	}
	return EdgeApp
}

// ValidateTunnelName gates edge hostnames (DNS-safe, no tunneling tricks).
func ValidateTunnelName(host string) error {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " /\\") {
		return fmt.Errorf("gateway: bad edge hostname %q", host)
	}
	return nil
}
