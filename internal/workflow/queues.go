// Durable queues + SSE stage feed (OCM-25): named work queues route tasks
// to executors, and every long operation publishes stages a UI can render
// as an explicit step list (never a generic spinner).
package workflow

import "fmt"

// Queue names for durable work.
const (
	QueueLifecycle  = "machine-lifecycle"
	QueueHost       = "host-maintenance"
	QueueArtifact   = "artifact-install"
	QueueReconcile  = "reconcile"
	QueueNotify     = "notifications"
	QueueConfigPush = "config-push"
	QueueEphemeral  = "ephemeral-run"
)

// Queues lists every known queue for validation and admin display.
func Queues() []string {
	return []string{QueueLifecycle, QueueHost, QueueArtifact, QueueReconcile, QueueNotify, QueueConfigPush, QueueEphemeral}
}

// ValidQueue reports whether name is a known queue.
func ValidQueue(name string) bool {
	for _, q := range Queues() {
		if q == name {
			return true
		}
	}
	return false
}

// Stage is one published step of a running operation.
type Stage struct {
	Name    string // e.g. provisioning, building, starting, verifying
	Status  string // pending | active | done | failed
	Message string
}

// StageFeed is the ordered, replayable stage list for one operation.
// Late subscribers receive the full history (SSE late-replay).
type StageFeed struct {
	OpID   string
	Stages []Stage
}

// Publish appends a stage; re-publishing the same active stage updates it
// in place instead of duplicating.
func (f *StageFeed) Publish(s Stage) {
	for i := range f.Stages {
		if f.Stages[i].Name == s.Name {
			f.Stages[i] = s
			return
		}
	}
	f.Stages = append(f.Stages, s)
}

// Failed reports whether any stage failed.
func (f *StageFeed) Failed() bool {
	for _, s := range f.Stages {
		if s.Status == "failed" {
			return true
		}
	}
	return false
}

// Validate gates feeds: operation identity required.
func (f StageFeed) Validate() error {
	if f.OpID == "" {
		return fmt.Errorf("workflow: stage feed needs an operation id")
	}
	return nil
}
