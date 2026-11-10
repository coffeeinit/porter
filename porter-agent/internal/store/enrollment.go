package store

import (
	"database/sql"
	"errors"
)

// JoinAttemptOutcome returns the last control-plane URL and enrollment outcome
// without retaining the hostname or either enrollment/node token.
func (s *Store) JoinAttemptOutcome() (string, string, bool, error) {
	var server string
	var outcome string
	err := s.db.QueryRow(`SELECT server,outcome FROM enrollment_attempt WHERE id=1`).Scan(&server, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return server, outcome, true, nil
}
