package ptyd

import (
	"testing"
	"time"
)

func sess(id, vm string) Session {
	return Session{ID: id, VMID: vm, Principal: "u1", OpenedAt: time.Now(), Cols: 80, Rows: 24}
}

func TestOpenClose(t *testing.T) {
	r := NewRegistry(1)
	if err := r.Open(sess("s1", "vm1")); err != nil {
		t.Fatal(err)
	}
	if err := r.Open(sess("s2", "vm1")); err == nil {
		t.Fatal("per-VM cap must reject")
	}
	if err := r.Open(sess("s3", "vm2")); err != nil {
		t.Fatal("other VMs unaffected:", err)
	}
	r.Close("s1")
	if err := r.Open(sess("s4", "vm1")); err != nil {
		t.Fatal("slot must free on close:", err)
	}
	r.Close("missing") // idempotent
}

func TestValidate(t *testing.T) {
	if err := (Session{}).Validate(); err == nil {
		t.Fatal("empty session must fail")
	}
	s := sess("s", "v")
	s.Cols = 0
	if err := s.Validate(); err == nil {
		t.Fatal("zero dims must fail")
	}
}
