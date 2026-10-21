package ldap

import (
	"fmt"
	"strings"
	"testing"
)

// NOTE: bind_test.go + bind_extra_test.go already cover fallback bind,
// unknown user, dial error, and UPN passthrough. These T12 cases cover the
// remaining Authenticate branches: fallback search error, UPN success with
// empty search, empty-domain verbatim bind, and fallback without memberOf.

// t12Conn is a configurable stub Conn: bindOK usernames succeed with
// password "secret"; search returns canned entries or a canned error.
type t12Conn struct {
	bindOK    map[string]bool
	entries   []Entry
	searchErr error
}

func (c t12Conn) Bind(username, password string) error {
	if c.bindOK[username] && password == "secret" {
		return nil
	}
	return fmt.Errorf("invalid credentials")
}

func (c t12Conn) Search(_ *SearchRequest) ([]Entry, error) {
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	return c.entries, nil
}

func (c t12Conn) Close() error { return nil }

type t12Dialer struct {
	conn Conn
	err  error
}

func (d t12Dialer) Dial(Config) (Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func t12Cfg() Config {
	return Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
}

func TestT12AuthenticateFallbackSearchError(t *testing.T) {
	conn := t12Conn{bindOK: map[string]bool{}, searchErr: fmt.Errorf("search boom")}
	_, _, err := Authenticate(t12Dialer{conn: conn}, t12Cfg(), "jdoe", "corp.x", "secret")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("UPN fail + search error must be auth failure, got %v", err)
	}
}

func TestT12AuthenticateUPNSuccessEmptySearch(t *testing.T) {
	conn := t12Conn{bindOK: map[string]bool{"jdoe@corp.x": true}, entries: nil}
	dn, groups, err := Authenticate(t12Dialer{conn: conn}, t12Cfg(), "jdoe", "corp.x", "secret")
	if err != nil {
		t.Fatalf("UPN bind success must win over empty search: %v", err)
	}
	if dn != "" || len(groups) != 0 {
		t.Fatalf("empty search must yield empty identity, got %q %v", dn, groups)
	}
}

func TestT12AuthenticateEmptyDomainVerbatim(t *testing.T) {
	conn := t12Conn{
		bindOK:  map[string]bool{"jdoe": true},
		entries: []Entry{{DN: "CN=jdoe,DC=x", Attrs: map[string][]string{"memberOf": {"CN=g,DC=x"}}}},
	}
	dn, groups, err := Authenticate(t12Dialer{conn: conn}, t12Cfg(), "jdoe", "", "secret")
	if err != nil {
		t.Fatalf("empty domain must bind login verbatim: %v", err)
	}
	if dn == "" || len(groups) != 1 {
		t.Fatalf("want dn+groups, got %q %v", dn, groups)
	}
}

func TestT12AuthenticateFallbackNoGroups(t *testing.T) {
	conn := t12Conn{
		bindOK:  map[string]bool{"CN=jdoe,DC=x": true},
		entries: []Entry{{DN: "CN=jdoe,DC=x", Attrs: map[string][]string{}}},
	}
	dn, groups, err := Authenticate(t12Dialer{conn: conn}, t12Cfg(), "jdoe", "corp.x", "secret")
	if err != nil {
		t.Fatalf("fallback without memberOf must succeed: %v", err)
	}
	if dn != "CN=jdoe,DC=x" {
		t.Fatalf("fallback DN wrong: %q", dn)
	}
	if len(groups) != 0 {
		t.Fatalf("missing memberOf must yield empty groups, got %v", groups)
	}
}
