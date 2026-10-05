package ldap

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	ok := Config{URL: "ldaps://ad.example.com:636", BaseDN: "dc=x,dc=y", Schema: SchemaAD}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{URL: "ldap://h", BaseDN: "b", Schema: SchemaLDAP}).Validate(); err == nil {
		t.Fatal("plaintext without StartTLS must fail")
	}
	if err := (Config{URL: "http://h", BaseDN: "b", Schema: SchemaAD}).Validate(); err == nil {
		t.Fatal("non-ldap scheme must fail")
	}
}

func TestFilters(t *testing.T) {
	ad := Config{Schema: SchemaAD}
	if f := ad.UserFilter("jdoe"); !strings.Contains(f, "sAMAccountName=jdoe") {
		t.Fatal("AD filter wrong:", f)
	}
	if f := ad.UserFilter("a*b"); !strings.Contains(f, `a\2ab`) {
		t.Fatal("injection must be escaped:", f)
	}
	ld := Config{Schema: SchemaLDAP}
	if f := ld.UserFilter("jdoe"); !strings.Contains(f, "uid=jdoe") {
		t.Fatal("LDAP filter wrong:", f)
	}
	if UPN("jdoe", "corp.x") != "jdoe@corp.x" {
		t.Fatal("UPN wrong")
	}
}
