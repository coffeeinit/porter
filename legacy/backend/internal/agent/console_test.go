package agent

import (
	"testing"
	"time"
)

func TestConsoleToken(t *testing.T) {
	key := []byte("test-signing-key-32-bytes-long!!")
	tok, err := MintConsoleToken(key, "vm-1", ScopeTerminal, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := VerifyConsoleToken(key, "vm-1", tok, time.Now())
	if err != nil || scope != ScopeTerminal {
		t.Fatalf("verify: %v scope=%q", err, scope)
	}
	if _, err := VerifyConsoleToken(key, "vm-2", tok, time.Now()); err == nil {
		t.Fatal("cross-vm must fail")
	}
	if _, err := VerifyConsoleToken([]byte("other-key"), "vm-1", tok, time.Now()); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := VerifyConsoleToken(key, "vm-1", tok, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expired must fail")
	}
	if _, err := MintConsoleToken(key, "vm-1", "shell", 5*time.Minute); err == nil {
		t.Fatal("unknown scope must fail")
	}
	if _, err := MintConsoleToken(nil, "vm-1", ScopeTerminal, 5*time.Minute); err == nil {
		t.Fatal("empty key must fail")
	}
}
