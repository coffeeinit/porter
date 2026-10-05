package egress

import (
	"testing"
)

func TestBypass(t *testing.T) {
	c := Config{Bypass: []string{".internal", "10.*", "proxy.local"}}
	for _, h := range []string{"svc.internal", "internal", "10.1.2.3", "proxy.local"} {
		if !c.Bypassed(h) {
			t.Fatalf("%s must bypass", h)
		}
	}
	if c.Bypassed("example.com") {
		t.Fatal("public host must not bypass")
	}
}

func TestValidate(t *testing.T) {
	if err := (Config{HTTPProxy: "http://p:8080"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{HTTPProxy: "://bad"}).Validate(); err == nil {
		t.Fatal("bad proxy URL must fail")
	}
}
