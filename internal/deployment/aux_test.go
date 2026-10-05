package deployment

import "testing"

func TestAuxPairing(t *testing.T) {
	l := &AuxLink{WorkloadID: "web", AuxID: "web-chrome", Kind: AuxChrome}
	if err := l.Advance(false); err != nil || l.State != PairPairing {
		t.Fatalf("unpaired->pairing: %v", err)
	}
	if err := l.Advance(false); err == nil {
		t.Fatal("pairing without compensations must fail")
	}
	l.Firewall, l.Proxied = true, true
	if err := l.Advance(false); err != nil || l.State != PairPaired {
		t.Fatalf("pairing->paired: %v", err)
	}
	if err := l.Advance(false); err != nil || l.State != PairUnpairing {
		t.Fatalf("paired->unpairing: %v", err)
	}
	if err := l.Advance(false); err == nil {
		t.Fatal("unpair waits for agent")
	}
	if err := l.Advance(true); err != nil || l.State != PairUnpaired {
		t.Fatalf("unpair complete: %v", err)
	}
	if l.Firewall || l.Proxied {
		t.Fatal("unpair must clear compensations")
	}
	bad := &AuxLink{WorkloadID: "x", AuxID: "x", Kind: AuxChrome}
	if err := bad.Advance(false); err == nil {
		t.Fatal("self-pair must fail")
	}
}
