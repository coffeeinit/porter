package store

import (
	"database/sql"
	"errors"
)

// JoinAttemptOutcome returns the last control-plane URL and enrollment outcome.
func (s *Store) JoinAttemptOutcome() (string, string, bool, error) {
	attempt, found, err := s.JoinAttemptRecovery()
	if err != nil || !found {
		return "", "", found, err
	}
	return attempt.Server, attempt.Outcome, true, nil
}

// JoinAttemptRecovery returns non-secret local recovery metadata. The join and
// node bearer credentials are never persisted in this record.
func (s *Store) JoinAttemptRecovery() (JoinAttempt, bool, error) {
	var attempt JoinAttempt
	err := s.db.QueryRow(`SELECT server,hostname,started_at,outcome FROM enrollment_attempt WHERE id=1`).Scan(
		&attempt.Server, &attempt.Hostname, &attempt.StartedAt, &attempt.Outcome,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinAttempt{}, false, nil
	}
	if err != nil {
		return JoinAttempt{}, false, err
	}
	return attempt, true, nil
}
