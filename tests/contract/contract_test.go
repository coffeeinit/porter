// Package contract holds cross-package invariants (PVE-23): the static
// contracts every subsystem must honor — exit-code ranges, queue allowlists,
// phase-machine terminality — asserted in one place so drift fails loudly.
package contract

import (
	"testing"

	"porter/internal/gateway"
	"porter/internal/storage"
	"porter/internal/workflow"
)

func TestExitCodeContract(t *testing.T) {
	for _, v := range []int{0, 1, 137, 255} {
		if _, err := workflow.ParseExitCode(v); err != nil {
			t.Fatalf("code %d must pass: %v", v, err)
		}
	}
	for _, v := range []int{-1, 256, 9999} {
		if _, err := workflow.ParseExitCode(v); err == nil {
			t.Fatalf("code %d must fail", v)
		}
	}
}

func TestQueueContract(t *testing.T) {
	// Every executor queue must be a known workflow queue.
	for _, q := range []string{workflow.QueueLifecycle, workflow.QueueEphemeral, workflow.QueueConfigPush} {
		if !workflow.ValidQueue(q) {
			t.Fatalf("queue %q unknown", q)
		}
	}
}

func TestEphemeralTerminal(t *testing.T) {
	r := workflow.EphemeralRun{JobID: "j1", Image: "alpine"}
	exit, _ := workflow.ParseExitCode(0)
	if err := r.Advance(nil); err != nil { // prepared -> booted
		t.Fatal(err)
	}
	if err := r.Advance(&exit); err != nil { // booted -> executed
		t.Fatal(err)
	}
	if err := r.Advance(nil); err != nil { // executed -> destroyed
		t.Fatal(err)
	}
	if !r.Destroyed() {
		t.Fatal("run must be terminal")
	}
}

func TestCloneAndEdgeContract(t *testing.T) {
	p := storage.ClonePlan{SourceVolume: "a", TargetVolume: "b", NewMAC: "m2", NewIP: "i2", SourceMAC: "m1", SourceIP: "i1"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if gateway.Decide("Googlebot", true) != gateway.EdgePrerender {
		t.Fatal("bot contract broken")
	}
}
