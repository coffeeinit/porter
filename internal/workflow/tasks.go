// Durable tasks for long-running operations (FCM-13/17, OCM-18): builds,
// deploys, snapshots, migrations. Tasks persist in Postgres (never in-memory
// maps) with idempotency keys, so controller restarts resume instead of
// duplicating work. DBOS-style checkpointing can adopt this state machine.
package workflow

import "fmt"

// Task states (SRS §54). PartiallySucceeded and NeedsAttention cover
// multi-step rollouts where some replicas lag.
const (
	StateQueued             = "QUEUED"
	StateRunning            = "RUNNING"
	StateWaiting            = "WAITING"
	StateSucceeded          = "SUCCEEDED"
	StateFailed             = "FAILED"
	StateRetrying           = "RETRYING"
	StateCancelled          = "CANCELLED"
	StatePartiallySucceeded = "PARTIALLY_SUCCEEDED"
	StateNeedsAttention     = "NEEDS_ATTENTION"
)

// terminal reports states that end a task's lifecycle. FAILED is not
// terminal: a failed task awaits retry-or-cancel, which Transition enforces.
func terminal(s string) bool {
	switch s {
	case StateSucceeded, StateCancelled, StatePartiallySucceeded, StateNeedsAttention:
		return true
	}
	return false
}

// Task is one durable operation.
type Task struct {
	ID             string
	IdempotencyKey string
	Kind           string // build | deploy | snapshot | backup | migrate | ...
	State          string
	Attempt        int
	Progress       Progress
	Error          string
}

// Progress is the FCM OperationProgress shape: pollable stage + fraction.
type Progress struct {
	Stage string
	Total int64
	Done  int64
}

// Percent returns 0..100 (0 when Total is 0).
func (p Progress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	q := p.Done * 100 / p.Total
	if q > 100 {
		q = 100
	}
	return int(q)
}

// Transition moves a task between states, rejecting illegal edges
// (terminal states never reopen; retries only flow through RETRYING).
func (t *Task) Transition(next string) error {
	if terminal(t.State) {
		return fmt.Errorf("workflow: task %s is terminal in %s", t.ID, t.State)
	}
	switch t.State {
	case "", StateQueued:
		if next != StateRunning && next != StateCancelled {
			return fmt.Errorf("workflow: QUEUED -> %s illegal", next)
		}
	case StateRunning:
		switch next {
		case StateWaiting, StateSucceeded, StateFailed, StateCancelled, StatePartiallySucceeded, StateNeedsAttention:
		default:
			return fmt.Errorf("workflow: RUNNING -> %s illegal", next)
		}
	case StateWaiting:
		if next != StateRunning && next != StateCancelled && next != StateFailed {
			return fmt.Errorf("workflow: WAITING -> %s illegal", next)
		}
	case StateFailed:
		if next != StateRetrying && next != StateCancelled {
			return fmt.Errorf("workflow: FAILED -> %s illegal (retry or cancel)", next)
		}
	case StateRetrying:
		if next != StateRunning && next != StateCancelled {
			return fmt.Errorf("workflow: RETRYING -> %s illegal", next)
		}
	default:
		return fmt.Errorf("workflow: unknown state %q", t.State)
	}
	if next == StateRetrying {
		t.Attempt++
	}
	t.State = next
	return nil
}
