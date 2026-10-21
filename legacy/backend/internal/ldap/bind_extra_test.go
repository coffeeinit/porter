package ldap

import (
	"fmt"
	"strings"
	"testing"
)

// searchFailConn binds one UPN but always fails searches, exercising the
// UPN-direct success path where group resolution is best-effort.
type searchFailConn struct {
	upn string
}

func (c searchFailConn) Bind(username, password string) error {
	if username == c.upn && password == "secret" {
		return nil
	}
	return fmt.Errorf("invalid credentials")
}

func (c searchFailConn) Search(_ *SearchRequest) ([]Entry, error) {
	return nil, fmt.Errorf("search boom")
}

func (c searchFailConn) Close() error { return nil }

func TestAuthenticateFallbackBind(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	conn := stubConn{
		bindOK: map[string]bool{"CN=jdoe,DC=x": true},
		users: map[string]Entry{
			"u": {DN: "CN=jdoe,DC=x", Attrs: map[string][]string{"memberOf": {"CN=g,DC=x"}}},
		},
	}
	dn, groups, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe", "corp.x", "secret")
	if err != nil {
		t.Fatalf("fallback bind must succeed: %v", err)
	}
	if dn != "CN=jdoe,DC=x" {
		t.Fatalf("fallback DN wrong: %q", dn)
	}
	if len(groups) != 1 || groups[0] != "CN=g,DC=x" {
		t.Fatalf("fallback groups wrong: %v", groups)
	}
}

func TestAuthenticateFallbackWrongPassword(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	conn := stubConn{
		bindOK: map[string]bool{"CN=jdoe,DC=x": true},
		users: map[string]Entry{
			"u": {DN: "CN=jdoe,DC=x", Attrs: map[string][]string{"memberOf": {"CN=g,DC=x"}}},
		},
	}
	if _, _, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe", "corp.x", "wrong"); err == nil {
		t.Fatal("fallback with wrong password must fail")
	}
}

func TestAuthenticateUnknownUser(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	conn := stubConn{bindOK: map[string]bool{}, users: map[string]Entry{}}
	_, _, err := Authenticate(stubDialer{conn: conn}, cfg, "ghost", "corp.x", "secret")
	if err == nil {
		t.Fatal("unknown user must fail")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error should name auth failure, got %v", err)
	}
}

func TestAuthenticateDialError(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	_, _, err := Authenticate(stubDialer{err: fmt.Errorf("dial boom")}, cfg, "jdoe", "corp.x", "secret")
	if err == nil || !strings.Contains(err.Error(), "dial boom") {
		t.Fatalf("dial error must propagate, got %v", err)
	}
}

func TestAuthenticateUPNPassthrough(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	conn := stubConn{
		bindOK: map[string]bool{"jdoe@corp.x": true},
		users: map[string]Entry{
			"u": {DN: "CN=jdoe,DC=x", Attrs: map[string][]string{"memberOf": {"CN=g,DC=x"}}},
		},
	}
	// Login already contains @: domain must not be appended.
	dn, groups, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe@corp.x", "other.example", "secret")
	if err != nil {
		t.Fatalf("UPN passthrough must succeed: %v", err)
	}
	if dn == "" || len(groups) != 1 {
		t.Fatalf("want dn+groups, got %q %v", dn, groups)
	}
	if got := UPN("jdoe@corp.x", "other.example"); got != "jdoe@corp.x" {
		t.Fatalf("UPN must passthrough full login, got %q", got)
	}
}

func TestAuthenticateUPNSuccessSearchErrorStillOK(t *testing.T) {
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	dn, groups, err := Authenticate(stubDialer{conn: searchFailConn{upn: "jdoe@corp.x"}}, cfg, "jdoe", "corp.x", "secret")
	if err != nil {
		t.Fatalf("UPN bind success must win over search error: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("search error must yield empty groups, got %v", groups)
	}
	_ = dn
}
