package metrics

import (
	"testing"
	"time"
)

func TestRetentionTiers(t *testing.T) {
	for _, tier := range []string{TierRaw, Tier10Min, TierHourly, TierDaily} {
		if _, err := Retention(tier); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Retention("nope"); err == nil {
		t.Fatal("unknown tier must fail")
	}
	ts := time.Date(2026, 9, 27, 10, 7, 33, 0, time.UTC)
	b, _ := Bucket(ts, Tier10Min)
	if b.Minute() != 0 {
		t.Fatalf("10m bucket wrong: %v", b)
	}
	d, _ := Bucket(ts, TierDaily)
	if d.Hour() != 0 || d.Day() != 27 {
		t.Fatalf("daily bucket wrong: %v", d)
	}
}

func TestSampleValidate(t *testing.T) {
	ok := Sample{VMID: "v", At: time.Now(), Micros: 10, MemMiB: 64}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.NetRx = -1
	if err := bad.Validate(); err == nil {
		t.Fatal("negative readings must fail")
	}
	if _, err := PartitionTable("1s"); err != nil {
		t.Fatal(err)
	}
	if _, err := PartitionTable("1y"); err == nil {
		t.Fatal("unknown grain must fail")
	}
}
