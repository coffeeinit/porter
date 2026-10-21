// Incident operations (SRS §36). Alerts evaluate in observability and route
// through alertjudge; this package owns incident severity, lifecycle and the
// postmortem-required close gate shared by the API and workflows.
package incident

import (
	"fmt"
	"time"
)

// Severities.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
)

// Statuses.
const (
	StatusOpen      = "open"
	StatusAcked     = "acknowledged"
	StatusMitigated = "mitigated"
	StatusResolved  = "resolved"
)

// Entry is one timeline record.
type Entry struct {
	At    time.Time
	Actor string
	Text  string
}

// Incident is the SRS §36 record: severity, timeline, affected resources,
// related deployments/nodes/alerts, remediation, postmortem.
type Incident struct {
	ID          string
	Severity    string
	Status      string
	Summary     string
	Affected    []string
	Remediation string
	Postmortem  string
	Timeline    []Entry
}

// New validates and opens an incident.
func New(id, severity, summary string) (*Incident, error) {
	switch severity {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
	default:
		return nil, fmt.Errorf("incident: unknown severity %q", severity)
	}
	if id == "" || summary == "" {
		return nil, fmt.Errorf("incident: id and summary are required")
	}
	return &Incident{ID: id, Severity: severity, Status: StatusOpen, Summary: summary}, nil
}

// Transition moves open → acknowledged → mitigated → resolved. Resolving
// requires remediation + postmortem notes (SRS §36 close gate).
func (in *Incident) Transition(next, actor, note string) error {
	want := ""
	switch in.Status {
	case StatusOpen:
		want = StatusAcked
	case StatusAcked:
		want = StatusMitigated
	case StatusMitigated:
		want = StatusResolved
	default:
		return fmt.Errorf("incident: %s is terminal", in.Status)
	}
	if next != want {
		return fmt.Errorf("incident: want %q next, got %q", want, next)
	}
	if next == StatusResolved && (in.Remediation == "" || in.Postmortem == "") {
		return fmt.Errorf("incident: resolve needs remediation + postmortem")
	}
	in.Status = next
	in.Timeline = append(in.Timeline, Entry{At: time.Now(), Actor: actor, Text: note})
	return nil
}

// Page reports whether the severity pages a human (critical/high only).
func Page(severity string) bool {
	return severity == SeverityCritical || severity == SeverityHigh
}
