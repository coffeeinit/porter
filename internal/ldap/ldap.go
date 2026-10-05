// Package ldap models LDAP/AD directory integration (FCM-22): LDAPS or
// StartTLS connections, UPN direct bind, sAMAccountName search with memberOf
// group mapping, and JIT user provisioning. Certificate verification is ON
// by default (SkipVerify is an explicit, audited escape hatch), and filters
// support both AD (sAMAccountName) and LDAP (uid) schemas.
package ldap

import (
	"fmt"
	"strings"
)

// Schema selects the directory flavor for user search filters.
type Schema string

const (
	SchemaAD   Schema = "ad"   // sAMAccountName + memberOf
	SchemaLDAP Schema = "ldap" // uid + memberOf
)

// Config is one directory connection.
type Config struct {
	URL        string // ldaps://host:636 or ldap://host:389 (+StartTLS)
	BindDN     string
	BaseDN     string
	Schema     Schema
	StartTLS   bool
	SkipVerify bool // escape hatch only; audited when true
}

// Validate gates config: URL scheme, base DN, and schema required.
func (c Config) Validate() error {
	if !strings.HasPrefix(c.URL, "ldaps://") && !strings.HasPrefix(c.URL, "ldap://") {
		return fmt.Errorf("ldap: URL must be ldaps:// or ldap://, got %q", c.URL)
	}
	if strings.HasPrefix(c.URL, "ldap://") && !c.StartTLS {
		return fmt.Errorf("ldap: plaintext ldap:// requires StartTLS")
	}
	if c.BaseDN == "" {
		return fmt.Errorf("ldap: base DN required")
	}
	switch c.Schema {
	case SchemaAD, SchemaLDAP:
	default:
		return fmt.Errorf("ldap: unknown schema %q", c.Schema)
	}
	return nil
}

// UserFilter builds the search filter for a login name.
func (c Config) UserFilter(login string) string {
	if c.Schema == SchemaAD {
		return fmt.Sprintf("(&(objectClass=user)(sAMAccountName=%s))", escape(login))
	}
	return fmt.Sprintf("(&(objectClass=inetOrgPerson)(uid=%s))", escape(login))
}

// GroupFilter builds the memberOf group search for a user DN.
func (c Config) GroupFilter(userDN string) string {
	return fmt.Sprintf("(&(objectClass=group)(member=%s))", escape(userDN))
}

// UPN renders user@domain for direct bind when Domain is set.
func UPN(login, domain string) string {
	if domain == "" || strings.Contains(login, "@") {
		return login
	}
	return login + "@" + domain
}

// GroupMapping maps a directory group DN to a Porter role.
type GroupMapping struct {
	GroupDN string
	Role    string
	Scope   string
}

// Validate gates mappings: all three fields required.
func (m GroupMapping) Validate() error {
	if m.GroupDN == "" || m.Role == "" || m.Scope == "" {
		return fmt.Errorf("ldap: mapping needs group DN, role and scope")
	}
	return nil
}

// escape quotes LDAP filter metacharacters (RFC 4515 minimal set).
func escape(s string) string {
	r := strings.NewReplacer(
		`\`, `\5c`,
		`*`, `\2a`,
		`(`, `\28`,
		`)`, `\29`,
		"\x00", `\00`,
	)
	return r.Replace(s)
}
