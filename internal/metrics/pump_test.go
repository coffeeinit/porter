package metrics

import (
	"errors"
	"testing"
	"time"
)

type stubSampler struct{ err error }

func (s stubSampler) Sample(vmID string) (Sample, error) {
	if s.err != nil {
		return Sample{}, s.err
	}
	return Sample{VMID: vmID, At: time.Now(), Micros: 250, MemMiB: 128}, nil
}

type stubSink struct{ records int }

func (s *stubSink) RecordUsage(string, string, string, float64, string, string) { s.records++ }

func TestPumpOnce(t *testing.T) {
	sink := &stubSink{}
	p := Pump{Sample: stubSampler{}, Sink: sink, Project: "p1"}
	if errs := p.PumpOnce([]string{"a", "b"}, time.Now()); len(errs) != 0 {
		t.Fatal(errs)
	}
	if sink.records != 10 { // cpu + mem + disk + rx + tx per VM
		t.Fatalf("want 10 records, got %d", sink.records)
	}
}

func TestPumpSkipsBadSampler(t *testing.T) {
	sink := &stubSink{}
	p := Pump{Sample: stubSampler{err: errors.New("no cgroup")}, Sink: sink}
	if errs := p.PumpOnce([]string{"a"}, time.Now()); len(errs) != 1 {
		t.Fatal("sampler error must surface, nothing fabricated")
	}
	if sink.records != 0 {
		t.Fatal("no readings, no records")
	}
}
