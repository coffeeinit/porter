package secretbox

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key := Key("test-material")
	box, err := Seal(key, []byte("s3cr3t-value"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Open(key, box)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, []byte("s3cr3t-value")) {
		t.Fatalf("round trip mismatch: %q", raw)
	}
}

func TestWrongKeyFails(t *testing.T) {
	box, err := Seal(Key("a"), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Key("b"), box); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestShortBoxFails(t *testing.T) {
	if _, err := Open(Key("a"), []byte{1, 2}); err == nil {
		t.Fatal("short box must fail")
	}
}

func TestKeyStable(t *testing.T) {
	if !bytes.Equal(Key("m"), Key("m")) {
		t.Fatal("key derivation must be stable")
	}
}
