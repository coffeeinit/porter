package certificate

import (
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	cur := PhaseRequested
	for _, want := range []string{PhaseValidating, PhaseIssuing, PhaseIssued, PhaseDeployed, PhaseRenewal, PhaseValidating} {
		next, err := Next(cur)
		if err != nil || next != want {
			t.Fatalf("Next(%s) = %q,%v want %q", cur, next, err, want)
		}
		cur = next
	}
	if _, err := Next(PhaseFailed); err == nil {
		t.Fatal("failed must be terminal")
	}
}

func TestNeedsRenewal(t *testing.T) {
	now := time.Now()
	if !NeedsRenewal(now.AddDate(0, 0, 10), now, 30) {
		t.Fatal("expiry inside window must renew")
	}
	if NeedsRenewal(now.AddDate(0, 0, 60), now, 30) {
		t.Fatal("expiry outside window must not renew")
	}
}
