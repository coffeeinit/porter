package ldap

import (
	"fmt"
	"testing"
)

type stubConn struct {
	bindOK map[string]bool
	users  map[string]Entry
}

func (s stubConn) Bind(username, password string) error {
	if s.bindOK[username] && password == "secret" {
		return nil
	}
	return fmt.Errorf("invalid credentials")
}

func (s stubConn) Search(req *SearchRequest) ([]Entry, error) {
	var out []Entry
	for _, e := range s.users {
		_ = e
		out = append(out, e)
	}
	return out, nil
}

func (s stubConn) Close() error { return nil }

type stubDialer struct {
	conn Conn
	err  error
}

func (s stubDialer) Dial(Config) (Conn, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.conn, nil
}

func TestAuthenticateUPN(t *testing.T) {
	conn := stubConn{
		bindOK: map[string]bool{"jdoe@corp.x": true},
		users: map[string]Entry{
			"u": {DN: "CN=jdoe,DC=x", Attrs: map[string][]string{"memberOf": {"CN=g,DC=x"}}},
		},
	}
	cfg := Config{URL: "ldaps://ad.x:636", BaseDN: "DC=x", Schema: SchemaAD}
	dn, groups, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe", "corp.x", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if dn == "" || len(groups) != 1 {
		t.Fatalf("want dn+groups, got %q %v", dn, groups)
	}
	if _, _, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe", "corp.x", "wrong"); err == nil {
		t.Fatal("wrong password must fail")
	}
	if _, _, err := Authenticate(stubDialer{conn: conn}, cfg, "jdoe", "corp.x", ""); err == nil {
		t.Fatal("empty password must fail")
	}
}
