package marketplace

import "testing"

func TestCredentialValidate(t *testing.T) {
	if err := (Credential{ID: "c", Kind: "api-key", Provider: "p", Ref: "secretbox:1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Credential{ID: "c", Kind: "password", Provider: "p", Ref: "r"}).Validate(); err == nil {
		t.Fatal("unknown kind must fail")
	}
	if err := (Invite{ID: "i", Email: "a@b.c", Role: "dev", Scope: "org:1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ActivityEvent{Actor: "u", Action: "deploy", Resource: "svc:1"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
