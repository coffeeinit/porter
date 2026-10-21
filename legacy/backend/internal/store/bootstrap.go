package store

// Enrollment-token read helpers (WP8). The enrollment_tokens table previously
// only had write paths (CreateEnrollmentToken, ConsumeEnrollmentToken); the
// API that mints one-time host bootstrap tokens had no way to inspect token
// state or differentiate an unknown token from a used/expired one at enroll
// time. Read-only additions — no schema changes.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"
)

// EnrollmentTokenRow is one persisted host bootstrap token (enrollment_tokens,
// migration 0026). Token carries the raw value only for single-token lookups;
// ListEnrollmentTokens masks it because the raw value is shown exactly once
// at mint time.
type EnrollmentTokenRow struct {
	Token     string    `json:"token"`
	Provider  string    `json:"provider"`
	Labels    string    `json:"labels"`
	UsedBy    string    `json:"used_by"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// GetEnrollmentToken returns one token row with its raw value, or ok=false
// when unknown. Callers pair it with ConsumeEnrollmentToken: the pre-check
// only names the precise failure (unknown vs used vs expired); the atomic
// UPDATE inside ConsumeEnrollmentToken stays the sole authority.
func (s *Store) GetEnrollmentToken(token string) (EnrollmentTokenRow, bool) {
	var row EnrollmentTokenRow
	err := s.pool.QueryRow(context.Background(), `
		SELECT token, provider, labels::text, used_by, expires_at, created_at
		FROM enrollment_tokens WHERE token = $1`, token).
		Scan(&row.Token, &row.Provider, &row.Labels, &row.UsedBy, &row.ExpiresAt, &row.CreatedAt)
	if err != nil {
		return EnrollmentTokenRow{}, false
	}
	return row, true
}

// ListEnrollmentTokens returns all bootstrap tokens, newest first, with the
// secret value masked (penr_…<last4>). Operators need token state (unused /
// consumed by which node / expired), never a second chance at the secret.
func (s *Store) ListEnrollmentTokens() []EnrollmentTokenRow {
	rows, err := s.pool.Query(context.Background(), `
		SELECT token, provider, labels::text, used_by, expires_at, created_at
		FROM enrollment_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]EnrollmentTokenRow, 0)
	for rows.Next() {
		var row EnrollmentTokenRow
		if err := rows.Scan(&row.Token, &row.Provider, &row.Labels, &row.UsedBy, &row.ExpiresAt, &row.CreatedAt); err != nil {
			continue
		}
		row.Token = maskEnrollmentToken(row.Token)
		out = append(out, row)
	}
	return out
}

// maskEnrollmentToken keeps the prefix and last four hex chars so operators
// can match a listed token against the mint-time value they stored.
func maskEnrollmentToken(token string) string {
	if len(token) <= 10 { // penr_ + fewer than 5 body chars: mask entirely
		return "penr_…"
	}
	return token[:5] + "…" + token[len(token)-4:]
}

// DeleteEnrollmentToken removes one token row (used-token housekeeping and
// fixture cleanup). Returns true when a row was deleted.
func (s *Store) DeleteEnrollmentToken(token string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM enrollment_tokens WHERE token = $1`, token)
	return err == nil && res.RowsAffected() > 0
}

// OrgIDByNameOrID resolves an org header value that may be a name or slug
// (e.g. "default" -> "Default organization") to the real org ID. orgs.id is
// a UUID column, so a raw name must never be persisted into org_id fields.
// Match order: exact ID, exact name, name prefix (deterministic by age).
// ok=false when no org matches.
func (s *Store) OrgIDByNameOrID(v string) (string, bool) {
	if s == nil || s.pool == nil {
		return "", false
	}
	row := s.pool.QueryRow(context.Background(),
		`SELECT id::text FROM orgs
		 WHERE id::text = $1 OR lower(name) = lower($1) OR lower(name) LIKE lower($1) || '%'
		 ORDER BY created_at LIMIT 1`, v)
	var id string
	if err := row.Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// SetServerNodeToken stores the sha256 of a node's heartbeat token in the
// server row's data JSONB. The plain token is returned once at enroll time
// and never persisted.
func (s *Store) SetServerNodeToken(serverID, sha256Hex string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE servers SET data = jsonb_set(COALESCE(data,'{}'), '{node_token_sha256}', to_jsonb($2::text)) WHERE id::text = $1`,
		serverID, sha256Hex)
	return err
}

// ServerNodeTokenOK reports whether plainToken matches the stored node token
// hash for the server. Unknown servers and unset hashes fail closed.
func (s *Store) ServerNodeTokenOK(serverID, plainToken string) bool {
	if serverID == "" || plainToken == "" || s.pool == nil {
		return false
	}
	var stored string
	err := s.pool.QueryRow(context.Background(),
		`SELECT data->>'node_token_sha256' FROM servers WHERE id::text = $1`, serverID).Scan(&stored)
	if err != nil || stored == "" {
		return false
	}
	sum := sha256.Sum256([]byte(plainToken))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(stored)) == 1
}
