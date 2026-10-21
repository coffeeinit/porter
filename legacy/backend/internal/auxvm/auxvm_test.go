package auxvm

import (
	"testing"
)

func TestValidate(t *testing.T) {
	if err := (AuxVM{ID: "a", WorkloadID: "w", Kind: KindBrowser}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (AuxVM{ID: "a", WorkloadID: "w", Kind: "toaster"}).Validate(); err == nil {
		t.Fatal("unknown kind must fail")
	}
}

func TestWebRTCPort(t *testing.T) {
	p, err := WebRTCPort(0)
	if err != nil || p != 56000 {
		t.Fatalf("slot 0 = 56000, got %d,%v", p, err)
	}
	if _, err := WebRTCPort(50); err == nil {
		t.Fatal("slot 50 out of range must fail")
	}
}

func TestCDPAuthorize(t *testing.T) {
	r := CDPRule{AuxVMID: "a", Paired: true, AllowedIP: []string{"10.0.0.9"}}
	if !r.Authorize("10.0.0.9") {
		t.Fatal("listed IP must pass")
	}
	if r.Authorize("10.0.0.10") {
		t.Fatal("unlisted IP must fail")
	}
	if (CDPRule{Paired: false, AllowedIP: []string{"10.0.0.9"}}).Authorize("10.0.0.9") {
		t.Fatal("unpaired must fail closed")
	}
}
