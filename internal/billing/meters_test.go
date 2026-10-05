package billing

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	ok := UsageEvent{CustomerID: "c1", ResourceID: "vm-1", Metric: MeterVCPUSeconds,
		Quantity: 3600, IdempotencyKey: "k1", At: time.Now()}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Metric = "magic_beans"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown meter must fail")
	}
	neg := ok
	neg.Quantity = -1
	if err := neg.Validate(); err == nil {
		t.Fatal("negative quantity must fail")
	}
	nokey := ok
	nokey.IdempotencyKey = ""
	if err := nokey.Validate(); err == nil {
		t.Fatal("missing idempotency key must fail")
	}
}

func TestDedupeAndMicrocents(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k1 := DedupeKey("s", "r", MeterNetBytes, at, 10)
	k2 := DedupeKey("s", "r", MeterNetBytes, at, 10)
	if k1 != k2 || k1 == "" {
		t.Fatal("dedupe key must be stable and non-empty")
	}
	if got := Microcents(2, 0.05); got != 100000 {
		t.Fatalf("want 100000µ¢, got %d", got)
	}
}
