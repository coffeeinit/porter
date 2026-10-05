package metadata

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestT14MintHexShape(t *testing.T) {
	n, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != NonceLen*2 {
		t.Fatalf("minted nonce must be %d hex chars, got %q", NonceLen*2, n)
	}
	raw, err := hex.DecodeString(n)
	if err != nil {
		t.Fatalf("minted nonce must be hex-decodable: %v", err)
	}
	if len(raw) != NonceLen {
		t.Fatalf("minted nonce must decode to %d bytes, got %d", NonceLen, len(raw))
	}
	// Bulk uniqueness: 64 mints, no collisions.
	seen := map[string]bool{n: true}
	for i := 0; i < 64; i++ {
		m, err := Mint()
		if err != nil {
			t.Fatal(err)
		}
		if seen[m] {
			t.Fatal("mint collision in 64 draws")
		}
		seen[m] = true
	}
}

func TestT14VerifyLengthMismatch(t *testing.T) {
	if !Verify("cafef00d", "cafef00d") {
		t.Fatal("equal values must verify")
	}
	// Different lengths must fail closed (no panic, no true).
	if Verify("abc", "abcd") {
		t.Fatal("length-mismatched nonces must not verify")
	}
	if Verify("abcd", "abc") {
		t.Fatal("length-mismatched nonces must not verify (reversed)")
	}
	// Case differs -> different bytes -> fail.
	if Verify("ABCDEF", "abcdef") {
		t.Fatal("case-differing nonces must not verify")
	}
	// Whitespace is significant, never trimmed.
	if Verify(" abc", "abc") {
		t.Fatal("padded nonce must not verify")
	}
}

func TestT14KernelArgRoundTrip(t *testing.T) {
	n, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	arg := KernelArg(n)
	if !strings.HasPrefix(arg, "metadata_nonce=") {
		t.Fatalf("kernel arg must carry metadata_nonce=, got %q", arg)
	}
	cmdline := "console=ttyS0 " + arg + " pci=off"
	got, ok := NonceFromCmdline(cmdline)
	if !ok || got != n {
		t.Fatalf("round trip failed: got %q,%v want %q,true", got, ok, n)
	}
	// Empty nonce kernel arg must fail closed on parse.
	if _, ok := NonceFromCmdline("metadata_nonce="); ok {
		t.Fatal("empty metadata_nonce= must fail closed")
	}
}

func TestT14NonceFromCmdlineEdges(t *testing.T) {
	// Nonce last, extra whitespace/tabs.
	n, ok := NonceFromCmdline("  console=ttyS0\tpci=off   metadata_nonce=zz99  ")
	if !ok || n != "zz99" {
		t.Fatalf("trailing nonce with spaces must parse, got %q,%v", n, ok)
	}
	// Empty string fails closed.
	if _, ok := NonceFromCmdline(""); ok {
		t.Fatal("empty cmdline must fail closed")
	}
	// Prefix-only lookalikes must not match.
	if _, ok := NonceFromCmdline("metadata_nonce2=abc x_metadata_nonce=def"); ok {
		t.Fatal("lookalike keys must not parse as metadata_nonce")
	}
	// Bare key without = fails closed.
	if _, ok := NonceFromCmdline("metadata_nonce"); ok {
		t.Fatal("bare key must fail closed")
	}
}

func TestT14CacheInvalidate(t *testing.T) {
	c := NewCache(0)
	c.Put("vm1", map[string]string{"K": "V"})
	c.Invalidate("vm1")
	if _, ok := c.Get("vm1"); ok {
		t.Fatal("invalidated entry must miss")
	}
	c.Invalidate("never-seen") // no-op, must not panic
}

func TestT14CachePutReplaces(t *testing.T) {
	c := NewCache(0)
	c.Put("vm1", map[string]string{"K": "old"})
	c.Put("vm1", map[string]string{"K": "new"})
	got, ok := c.Get("vm1")
	if !ok || got["K"] != "new" {
		t.Fatalf("put must replace, got %+v,%v", got, ok)
	}
}

func TestT14CacheCustomTTL(t *testing.T) {
	c := NewCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Put("vm1", map[string]string{"K": "V"})
	now = now.Add(59 * time.Second)
	if _, ok := c.Get("vm1"); !ok {
		t.Fatal("entry inside custom TTL must hit")
	}
	now = now.Add(2 * time.Second) // 61s total
	if _, ok := c.Get("vm1"); ok {
		t.Fatal("entry past custom TTL must miss")
	}
	// Expired entries evict on read: second read still misses.
	if _, ok := c.Get("vm1"); ok {
		t.Fatal("evicted entry must stay missing")
	}
}

func TestT14CacheMissUnknown(t *testing.T) {
	c := NewCache(0)
	if _, ok := c.Get("nope"); ok {
		t.Fatal("unknown vm must miss")
	}
	// VMs are isolated.
	c.Put("a", map[string]string{"K": "V"})
	if _, ok := c.Get("b"); ok {
		t.Fatal("other vm must miss")
	}
}
