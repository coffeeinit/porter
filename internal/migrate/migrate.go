// Package migrate implements cold workload migration (FCM-24): a planned,
// audited move of a stopped workload (snapshot + config) from a source node
// to a target node with digest verification. Live migration is explicitly
// out of scope: the VM stops on source before any byte moves, and traffic
// only shifts after the target verifies healthy. Transport is mTLS plus the
// ObjectStore — never raw TCP with bearer keys.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Phases in lifecycle order.
const (
	PhaseLock     = "lock"
	PhaseStop     = "stop"
	PhaseSnapshot = "snapshot"
	PhaseCopy     = "copy"
	PhaseVerify   = "verify"
	PhaseStart    = "start"
	PhaseDone     = "done"
)

// Plan is one migration's durable intent.
type Plan struct {
	VMID       string
	SourceNode string
	TargetNode string
	Phase      string
	SnapshotID string
	Digest     string // sha256 of the snapshot payload, hex
}

// Validate gates plans: same-node moves are rejected, identity required.
func (p Plan) Validate() error {
	if p.VMID == "" || p.SourceNode == "" || p.TargetNode == "" {
		return fmt.Errorf("migrate: plan needs vm, source and target")
	}
	if p.SourceNode == p.TargetNode {
		return fmt.Errorf("migrate: source and target must differ")
	}
	return nil
}

// Advance moves the plan forward exactly one phase.
func (p *Plan) Advance() error {
	next := map[string]string{
		"":            PhaseLock,
		PhaseLock:     PhaseStop,
		PhaseStop:     PhaseSnapshot,
		PhaseSnapshot: PhaseCopy,
		PhaseCopy:     PhaseVerify,
		PhaseVerify:   PhaseStart,
		PhaseStart:    PhaseDone,
	}[p.Phase]
	if next == "" {
		return fmt.Errorf("migrate: unknown or terminal phase %q", p.Phase)
	}
	p.Phase = next
	return nil
}

// Mover executes the physical move behind a Plan: snapshot the source,
// copy the payload to the target, verify and start there. Implementations
// ride the ObjectStore + runtime; the driver below only advances phases.
type Mover interface {
	Snapshot(vmID string) (snapshotID, digest string, err error)
	Copy(snapshotID, targetNode string) error
	VerifyStart(vmID, targetNode, digest string) error
}

// DigestOf returns the hex sha256 of a payload for verify-before-start.
func DigestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Verified reports whether payload matches the plan digest. Empty plan
// digest never verifies (fail closed).
func (p Plan) Verified(payload []byte) bool {
	if p.Digest == "" {
		return false
	}
	return DigestOf(payload) == p.Digest
}
