package metadata

import (
	"testing"
	"time"
)

func TestMintUnique(t *testing.T) {
	a, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != NonceLen*2 {
		t.Fatalf("nonces must be unique 32-hex: %q %q", a, b)
	}
}

func TestVerify(t *testing.T) {
	if !Verify("abc", "abc") {
		t.Fatal("equal nonces must verify")
	}
	if Verify("abc", "abd") {
		t.Fatal("different nonces must not verify")
	}
	if Verify("", "abc") || Verify("abc", "") {
		t.Fatal("empty values must fail closed")
	}
}

func TestCacheExpiry(t *testing.T) {
	c := NewCache(0)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Put("vm1", map[string]string{"K": "V"})
	if _, ok := c.Get("vm1"); !ok {
		t.Fatal("fresh entry must hit")
	}
	now = now.Add(SecretCacheTTL + time.Second)
	if _, ok := c.Get("vm1"); ok {
		t.Fatal("expired entry must miss")
	}
}

func TestNonceFromCmdline(t *testing.T) {
	n, ok := NonceFromCmdline("console=ttyS0 metadata_nonce=deadbeef pci=off")
	if !ok || n != "deadbeef" {
		t.Fatalf("got %q,%v", n, ok)
	}
	if _, ok := NonceFromCmdline("console=ttyS0 pci=off"); ok {
		t.Fatal("absent nonce must fail closed")
	}
}
