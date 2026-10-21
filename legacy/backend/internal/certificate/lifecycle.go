// Certificate lifecycle (SRS §30). Issuance executes in tls (ACME) with
// DNS-01 via dns; this package owns the phase machine and renewal timing
// shared by the domain controller and expiry monitoring.
package certificate

import (
	"fmt"
	"time"
)

// Lifecycle phases (SRS §30).
const (
	PhaseRequested  = "Requested"
	PhaseValidating = "Validating"
	PhaseIssuing    = "Issuing"
	PhaseIssued     = "Issued"
	PhaseDeployed   = "Deployed"
	PhaseRenewal    = "Renewal"
	PhaseFailed     = "Failed"
)

// Challenges.
const (
	ChallengeHTTP01 = "http-01"
	ChallengeDNS01  = "dns-01"
)

// Next advances the lifecycle. Deployed may re-enter Renewal; Failed is
// terminal for the attempt (a new attempt starts at Requested).
func Next(cur string) (string, error) {
	switch cur {
	case PhaseRequested:
		return PhaseValidating, nil
	case PhaseValidating:
		return PhaseIssuing, nil
	case PhaseIssuing:
		return PhaseIssued, nil
	case PhaseIssued:
		return PhaseDeployed, nil
	case PhaseDeployed:
		return PhaseRenewal, nil
	case PhaseRenewal:
		return PhaseValidating, nil
	default:
		return "", fmt.Errorf("certificate: bad phase %q", cur)
	}
}

// NeedsRenewal reports whether expiry falls within the renew window
// (default 30 days for ACME, 14 for short-lived).
func NeedsRenewal(expiresAt, now time.Time, windowDays int) bool {
	if windowDays <= 0 {
		windowDays = 30
	}
	return !expiresAt.After(now.AddDate(0, 0, windowDays))
}
