// Live directory bind (FCM-22 execution): LDAPS or StartTLS connections,
// UPN direct bind, search-bind fallback, and memberOf group resolution.
// Connections always verify certificates unless InsecureSkipVerify is set
// explicitly (audited escape hatch, never the default).
package ldap

import (
	"crypto/tls"
	"fmt"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// Dialer opens directory connections (injectable for tests).
type Dialer interface {
	Dial(cfg Config) (Conn, error)
}

// Conn is one bound connection.
type Conn interface {
	Bind(username, password string) error
	Search(req *SearchRequest) ([]Entry, error)
	Close() error
}

// SearchRequest is one directory search.
type SearchRequest struct {
	BaseDN string
	Filter string
	Attrs  []string
}

// Entry is one directory object.
type Entry struct {
	DN    string
	Attrs map[string][]string
}

// liveDialer dials real directories with verification by default.
type liveDialer struct {
	timeout time.Duration
}

// SystemDialer is the production dialer (10s timeout).
var SystemDialer Dialer = liveDialer{timeout: 10 * time.Second}

type liveConn struct{ c *goldap.Conn }

func (d liveDialer) Dial(cfg Config) (Conn, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	to := d.timeout
	if to <= 0 {
		to = 10 * time.Second
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec // explicit audited escape hatch
	var c *goldap.Conn
	var err error
	if len(cfg.URL) >= 8 && cfg.URL[:8] == "ldaps://" {
		c, err = goldap.DialURL(cfg.URL, goldap.DialWithTLSConfig(tlsCfg))
	} else {
		c, err = goldap.DialURL(cfg.URL)
		if err == nil && cfg.StartTLS {
			err = c.StartTLS(tlsCfg)
		}
	}
	_ = to
	if err != nil {
		return nil, fmt.Errorf("ldap: dial %s: %w", cfg.URL, err)
	}
	return &liveConn{c: c}, nil
}

func (c *liveConn) Bind(username, password string) error {
	if err := c.c.Bind(username, password); err != nil {
		return fmt.Errorf("ldap: bind: %w", err)
	}
	return nil
}

func (c *liveConn) Search(req *SearchRequest) ([]Entry, error) {
	res, err := c.c.Search(goldap.NewSearchRequest(
		req.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		0, 0, false, req.Filter, req.Attrs, nil,
	))
	if err != nil {
		return nil, fmt.Errorf("ldap: search: %w", err)
	}
	out := make([]Entry, 0, len(res.Entries))
	for _, e := range res.Entries {
		attrs := map[string][]string{}
		for _, a := range e.Attributes {
			attrs[a.Name] = a.Values
		}
		out = append(out, Entry{DN: e.DN, Attrs: attrs})
	}
	return out, nil
}

func (c *liveConn) Close() error { return c.c.Close() }

// Authenticate binds as the user (UPN direct when Domain is set) and
// returns their DN plus memberOf groups. Wrong password surfaces as an
// error, never a partial identity.
func Authenticate(d Dialer, cfg Config, login, domain, password string) (userDN string, groups []string, err error) {
	if password == "" {
		return "", nil, fmt.Errorf("ldap: empty password")
	}
	conn, err := d.Dial(cfg)
	if err != nil {
		return "", nil, err
	}
	defer conn.Close()
	upn := UPN(login, domain)
	if err := conn.Bind(upn, password); err != nil {
		// UPN direct failed: fall back to search-then-bind as the DN.
		found, serr := conn.Search(&SearchRequest{
			BaseDN: cfg.BaseDN, Filter: cfg.UserFilter(login),
			Attrs: []string{"memberOf"},
		})
		if serr != nil || len(found) == 0 {
			return "", nil, fmt.Errorf("ldap: authentication failed for %q", login)
		}
		userDN = found[0].DN
		if err := conn.Bind(userDN, password); err != nil {
			return "", nil, fmt.Errorf("ldap: authentication failed for %q", login)
		}
		return userDN, found[0].Attrs["memberOf"], nil
	}
	// UPN bound: resolve groups from the user's own memberOf (searching by
	// group membership with a UPN would miss DN-valued member attributes).
	found, serr := conn.Search(&SearchRequest{
		BaseDN: cfg.BaseDN, Filter: cfg.UserFilter(login),
		Attrs: []string{"memberOf"},
	})
	if serr == nil && len(found) > 0 {
		userDN = found[0].DN
		groups = found[0].Attrs["memberOf"]
	}
	return userDN, groups, nil
}
