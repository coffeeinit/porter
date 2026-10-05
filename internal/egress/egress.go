// Package egress implements controlled outbound access (FCM-16): static
// proxy configuration with suffix/wildcard bypass lists for air-gapped or
// filtered hosts (image pulls, catalog sync, version-train checks). Secrets
// (proxy auth) live in the secret store, never in this config.
package egress

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config is the static egress policy for one host.
type Config struct {
	HTTPProxy  string   // e.g. http://proxy:8080 (empty = direct)
	HTTPSProxy string   // e.g. http://proxy:8080 (empty = direct)
	Bypass     []string // suffix (".internal"), wildcard ("10.*"), or exact host
}

// Validate gates config: proxy URLs must parse when set.
func (c Config) Validate() error {
	for _, raw := range []string{c.HTTPProxy, c.HTTPSProxy} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("egress: bad proxy URL %q", raw)
		}
	}
	return nil
}

// Bypassed reports whether host skips the proxy.
func (c Config) Bypassed(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, rule := range c.Bypass {
		r := strings.ToLower(strings.TrimSpace(rule))
		if r == "" {
			continue
		}
		switch {
		case strings.HasPrefix(r, "."):
			if h == r[1:] || strings.HasSuffix(h, r) {
				return true
			}
		case strings.HasSuffix(r, "*"):
			if strings.HasPrefix(h, strings.TrimSuffix(r, "*")) {
				return true
			}
		default:
			if h == r {
				return true
			}
		}
	}
	return false
}

// Client builds an http.Client honoring the policy: bypassed hosts go
// direct, everything else goes through the configured proxy.
func (c Config) Client(timeout time.Duration) *http.Client {
	proxyFn := func(req *http.Request) (*url.URL, error) {
		if c.Bypassed(req.URL.Hostname()) {
			return nil, nil
		}
		raw := c.HTTPProxy
		if req.URL.Scheme == "https" && c.HTTPSProxy != "" {
			raw = c.HTTPSProxy
		}
		if raw == "" {
			return nil, nil
		}
		return url.Parse(raw)
	}
	return &http.Client{Transport: &http.Transport{Proxy: proxyFn}, Timeout: timeout}
}
