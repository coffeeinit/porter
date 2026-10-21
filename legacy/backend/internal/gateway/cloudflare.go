// Cloudflare tunnel + DNS client on a user-supplied API token (the free
// path): customers paste a scoped CF token, Porter creates the per-workload
// tunnel and its proxied CNAME (orange cloud) and reaps orphans. Tokens are
// secret-store references here — the raw value only lives in memory for the
// call. Cloudflare stays optional: TunnelDirect bypasses this file entirely.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cloudflare API base.
const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// CFClient speaks the subset of the Cloudflare v4 API Porter needs.
// BaseURL is overrideable (tests, self-hosted shims); Token is the
// user-supplied scoped API token, held in memory only. ProxyURL routes API
// calls through the configured egress proxy (empty = direct).
type CFClient struct {
	BaseURL  string
	Token    string
	Account  string // account id owning tunnels/zones
	ProxyURL string
	HTTP     *http.Client
}

func (c CFClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return cloudflareAPI
}

func (c CFClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	transport := &http.Transport{}
	if c.ProxyURL != "" {
		if u, err := url.Parse(c.ProxyURL); err == nil && u.Host != "" {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfMessage     `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfMessage struct {
	Message string `json:"message"`
}

func (c CFClient) call(method, path string, body any) (json.RawMessage, error) {
	if c.Token == "" {
		return nil, fmt.Errorf("gateway: cloudflare needs a user API token")
	}
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	var rdr io.Reader
	if raw != nil {
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base()+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var env cfEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("gateway: cloudflare bad response: %w", err)
	}
	if !env.Success {
		msg := "unknown"
		if len(env.Errors) > 0 {
			msg = env.Errors[0].Message
		}
		return nil, fmt.Errorf("gateway: cloudflare error: %s", msg)
	}
	return env.Result, nil
}

// TunnelRecord is one created tunnel. Token runs the connector
// (`cloudflared service install <token>` or docker run ... --token).
type TunnelRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// CreateTunnel provisions a remotely-managed tunnel (config_src:
// cloudflare) so ingress comes from the API, not a local config file.
func (c CFClient) CreateTunnel(name string) (TunnelRecord, error) {
	if name == "" {
		return TunnelRecord{}, fmt.Errorf("gateway: tunnel needs a name")
	}
	raw, err := c.call(http.MethodPost, "/accounts/"+c.Account+"/cfd_tunnel", map[string]string{
		"name": name, "config_src": "cloudflare",
	})
	if err != nil {
		return TunnelRecord{}, err
	}
	var rec TunnelRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return TunnelRecord{}, err
	}
	return rec, nil
}

// IngressRule maps one public hostname to a local service; the last rule
// must always be the catch-all (Cloudflare rejects lists without one).
type IngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

// PutIngressConfig replaces the remote ingress list (localhost → custom
// domain flow: tunnel → ingress → proxied CNAME → run connector).
func (c CFClient) PutIngressConfig(tunnelID string, rules []IngressRule) error {
	if tunnelID == "" || len(rules) == 0 {
		return fmt.Errorf("gateway: ingress needs tunnel and at least one rule")
	}
	last := rules[len(rules)-1]
	if last.Hostname != "" || (last.Service != "http_status:404" && !strings.HasPrefix(last.Service, "http_status:")) {
		return fmt.Errorf("gateway: last ingress rule must be the catch-all")
	}
	_, err := c.call(http.MethodPut,
		"/accounts/"+c.Account+"/cfd_tunnel/"+tunnelID+"/configurations",
		map[string]any{"config": map[string]any{"ingress": rules}})
	return err
}

// DeleteTunnel removes a tunnel; idempotent downstream (caller treats
// not-found as success — see Reap).
func (c CFClient) DeleteTunnel(tunnelID string) error {
	_, err := c.call(http.MethodDelete, "/accounts/"+c.Account+"/cfd_tunnel/"+tunnelID, nil)
	return err
}

// UpsertCNAME creates/updates the proxied (orange-cloud) CNAME
// hostname -> target inside zoneID.
func (c CFClient) UpsertCNAME(zoneID, hostname, target string) error {
	if zoneID == "" || hostname == "" || target == "" {
		return fmt.Errorf("gateway: CNAME needs zone, hostname and target")
	}
	_, err := c.call(http.MethodPost, "/zones/"+zoneID+"/dns_records", map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"proxied": true,
		"ttl":     1,
	})
	return err
}

// UpsertA creates/updates a proxied (orange-cloud) A record. proxied=true
// hides the origin behind Cloudflare; false exposes DNS-only mode.
func (c CFClient) UpsertA(zoneID, hostname, ip string, proxied bool) error {
	if zoneID == "" || hostname == "" || ip == "" {
		return fmt.Errorf("gateway: A record needs zone, hostname and ip")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("gateway: bad ip %q", ip)
	}
	_, err := c.call(http.MethodPost, "/zones/"+zoneID+"/dns_records", map[string]any{
		"type":    "A",
		"name":    hostname,
		"content": ip,
		"proxied": proxied,
		"ttl":     1,
	})
	return err
}

// UpsertTXT creates/updates a TXT record (domain verification, ACME DNS-01).
// TXT is never proxied.
func (c CFClient) UpsertTXT(zoneID, hostname, value string) error {
	if zoneID == "" || hostname == "" || value == "" {
		return fmt.Errorf("gateway: TXT needs zone, hostname and value")
	}
	_, err := c.call(http.MethodPost, "/zones/"+zoneID+"/dns_records", map[string]any{
		"type":    "TXT",
		"name":    hostname,
		"content": value,
		"proxied": false,
		"ttl":     120,
	})
	return err
}

// zoneRecord is one /zones result row.
type zoneRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FindZoneID resolves the zone id for a domain (exact match, case-insensitive).
func (c CFClient) FindZoneID(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", fmt.Errorf("gateway: empty domain")
	}
	raw, err := c.call(http.MethodGet, "/zones?name="+url.QueryEscape(domain), nil)
	if err != nil {
		return "", err
	}
	var zones []zoneRecord
	if err := json.Unmarshal(raw, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if strings.ToLower(z.Name) == domain && z.ID != "" {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("gateway: no Cloudflare zone for %q", domain)
}

// DeleteDNSRecord removes one DNS record by id (idempotent downstream).
func (c CFClient) DeleteDNSRecord(zoneID, recordID string) error {
	if zoneID == "" || recordID == "" {
		return fmt.Errorf("gateway: delete needs zone and record id")
	}
	_, err := c.call(http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil)
	return err
}
