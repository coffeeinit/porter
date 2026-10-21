package bench

import "testing"

func TestSLOFor(t *testing.T) {
	s, ok := SLOFor("alpine")
	if !ok || s.Agent <= 0 {
		t.Fatal("alpine SLO must exist")
	}
	if _, ok := SLOFor("toaster-os"); ok {
		t.Fatal("unknown image must miss")
	}
}

func TestMinRAM(t *testing.T) {
	m, err := MinRAM("systemd")
	if err != nil || m != 256 {
		t.Fatalf("got %d,%v", m, err)
	}
	if _, err := MinRAM("nope"); err == nil {
		t.Fatal("unknown class must fail")
	}
}
