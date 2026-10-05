package agent

import (
	"strings"
	"testing"
	"time"
)

func TestEnrollmentToken(t *testing.T) {
	tok, err := NewEnrollmentToken("hetzner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateToken(tok.Token); err != nil {
		t.Fatal(err)
	}
	if err := tok.Consumable(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToken("bogus"); err == nil {
		t.Fatal("bogus token must fail")
	}
	expired := tok
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if err := expired.Consumable(time.Now()); err == nil {
		t.Fatal("expired token must fail")
	}
	used := tok
	used.UsedBy = "node-1"
	if err := used.Consumable(time.Now()); err == nil {
		t.Fatal("used token must fail")
	}
}

func TestUnitName(t *testing.T) {
	if got := UnitName("AbC_123"); got != "porter-vm-abc123.service" {
		t.Fatalf("bad unit %q", got)
	}
	if !strings.HasSuffix(UnitName(""), ".service") {
		t.Fatal("empty id must still yield a unit")
	}
}

func TestHeartbeatStale(t *testing.T) {
	h := Heartbeat{NodeID: "n1", At: time.Now().Add(-4 * time.Minute)}
	if !h.StaleAfter(time.Now(), 3*time.Minute) {
		t.Fatal("old heartbeat must be stale")
	}
	if h.StaleAfter(time.Now(), 10*time.Minute) {
		t.Fatal("fresh-enough heartbeat must not be stale")
	}
}

func TestShouldUpdate(t *testing.T) {
	if ShouldUpdate("1.2.0", "1.2.0") || ShouldUpdate("1.2.0", "") || !ShouldUpdate("", "1.2.0") {
		t.Fatal("update check broken")
	}
}
