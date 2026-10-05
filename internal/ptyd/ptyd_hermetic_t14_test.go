package ptyd

import (
	"fmt"
	"testing"
)

func TestT14RegistryDuplicateID(t *testing.T) {
	r := NewRegistry(4)
	if err := r.Open(sess("dup", "vm1")); err != nil {
		t.Fatal(err)
	}
	// Same ID on another VM is still a duplicate.
	if err := r.Open(sess("dup", "vm2")); err == nil {
		t.Fatal("duplicate session ID must be rejected across VMs")
	}
	if got := r.Count("vm1"); got != 1 {
		t.Fatalf("duplicate must not add a session, count=%d", got)
	}
	if got := r.Count("vm2"); got != 0 {
		t.Fatalf("duplicate must not leak into vm2, count=%d", got)
	}
}

func TestT14RegistryDefaultCap(t *testing.T) {
	for _, n := range []int{0, -3} {
		r := NewRegistry(n)
		for i := 0; i < 4; i++ {
			if err := r.Open(sess(fmt.Sprintf("s%d", i), "vm1")); err != nil {
				t.Fatalf("NewRegistry(%d) must default to 4, failed at %d: %v", n, i, err)
			}
		}
		if err := r.Open(sess("overflow", "vm1")); err == nil {
			t.Fatalf("NewRegistry(%d) default cap 4 must reject 5th session", n)
		}
	}
}

func TestT14RegistryCountAndFree(t *testing.T) {
	r := NewRegistry(2)
	if got := r.Count("vm9"); got != 0 {
		t.Fatalf("empty registry count must be 0, got %d", got)
	}
	if err := r.Open(sess("a", "vm9")); err != nil {
		t.Fatal(err)
	}
	if err := r.Open(sess("b", "vm9")); err != nil {
		t.Fatal(err)
	}
	if got := r.Count("vm9"); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
	r.Close("a")
	if got := r.Count("vm9"); got != 1 {
		t.Fatalf("close must free one slot, got %d", got)
	}
	if err := r.Open(sess("c", "vm9")); err != nil {
		t.Fatalf("freed slot must accept new session: %v", err)
	}
}

func TestT14RegistryCloseIdempotentDouble(t *testing.T) {
	r := NewRegistry(2)
	if err := r.Open(sess("x", "vm1")); err != nil {
		t.Fatal(err)
	}
	r.Close("x")
	r.Close("x") // second close is a no-op
	r.Close("")  // empty id is a no-op
	if got := r.Count("vm1"); got != 0 {
		t.Fatalf("want 0 after closes, got %d", got)
	}
	if err := r.Open(sess("y", "vm1")); err != nil {
		t.Fatalf("slot must be free after close: %v", err)
	}
}

func TestT14SessionValidateEdges(t *testing.T) {
	base := sess("s", "v")
	cases := []struct {
		name    string
		mutate  func(*Session)
		wantErr bool
	}{
		{"valid", func(s *Session) {}, false},
		{"missing id", func(s *Session) { s.ID = "" }, true},
		{"missing vmid", func(s *Session) { s.VMID = "" }, true},
		{"missing principal", func(s *Session) { s.Principal = "" }, true},
		{"zero cols", func(s *Session) { s.Cols = 0 }, true},
		{"zero rows", func(s *Session) { s.Rows = 0 }, true},
		{"negative cols", func(s *Session) { s.Cols = -1 }, true},
		{"negative rows", func(s *Session) { s.Rows = -80 }, true},
	}
	for _, c := range cases {
		s := base
		c.mutate(&s)
		if err := s.Validate(); (err != nil) != c.wantErr {
			t.Fatalf("%s: wantErr=%v got %v", c.name, c.wantErr, err)
		}
	}
}

func TestT14RegistryRejectsInvalid(t *testing.T) {
	r := NewRegistry(4)
	bad := sess("bad", "vm1")
	bad.Principal = ""
	if err := r.Open(bad); err == nil {
		t.Fatal("open must run Validate and reject principal-less sessions")
	}
	if err := r.Open(Session{}); err == nil {
		t.Fatal("open must reject empty sessions")
	}
	if got := r.Count("vm1"); got != 0 {
		t.Fatalf("rejected opens must not count, got %d", got)
	}
}

func TestT14RegistryPerVMIsolation(t *testing.T) {
	r := NewRegistry(1)
	if err := r.Open(sess("v1s1", "vm1")); err != nil {
		t.Fatal(err)
	}
	// vm1 at cap; vm2 unaffected and vice versa.
	if err := r.Open(sess("v1s2", "vm1")); err == nil {
		t.Fatal("vm1 cap must hold")
	}
	if err := r.Open(sess("v2s1", "vm2")); err != nil {
		t.Fatalf("vm2 must be independent of vm1 cap: %v", err)
	}
	if got := r.Count("vm1"); got != 1 {
		t.Fatalf("vm1 count=%d want 1", got)
	}
	if got := r.Count("vm2"); got != 1 {
		t.Fatalf("vm2 count=%d want 1", got)
	}
}

func TestT14RegistryConstants(t *testing.T) {
	if Port != 7681 {
		t.Fatalf("ptyd port must stay 7681, got %d", Port)
	}
	if Scope != "terminal" {
		t.Fatalf("ptyd scope must stay terminal, got %q", Scope)
	}
}
