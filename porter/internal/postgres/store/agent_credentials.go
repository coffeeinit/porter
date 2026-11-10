package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math"
	"time"
)

const maxCredentialRotationGrace = 30 * time.Minute

// NodeCredentialIssue returns a bearer secret exactly once to the provisioning
// caller. Only its SHA-256 hash is persisted by the store.
type NodeCredentialIssue struct {
	NodeID             string     `json:"node_id"`
	Token              string     `json:"token"`
	Generation         int64      `json:"generation"`
	PreviousValidUntil *time.Time `json:"previous_valid_until,omitempty"`
}

// ProvisionNodeCredential creates a 256-bit node credential or rotates the
// current one. Prior active credentials stay valid only through rotationGrace;
// the caller must deliver Token securely and must not log it.
func (s *Store) ProvisionNodeCredential(ctx context.Context, nodeID string, now time.Time, rotationGrace time.Duration) (NodeCredentialIssue, error) {
	if err := validateOperationID("node", nodeID); err != nil {
		return NodeCredentialIssue{}, err
	}
	if now.IsZero() || rotationGrace < 0 || rotationGrace > maxCredentialRotationGrace {
		return NodeCredentialIssue{}, fmt.Errorf("%w: invalid node credential time or rotation grace", ErrInvalidArgument)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return NodeCredentialIssue{}, fmt.Errorf("generate node credential: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	tokenHash := sha256.Sum256([]byte(token))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeCredentialIssue{}, fmt.Errorf("begin node credential provisioning: %w", err)
	}
	defer tx.Rollback()
	var nodeState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&nodeState); err != nil {
		return NodeCredentialIssue{}, notFound("Fleet node", nodeID, err)
	}

	var generation int64
	var hasCurrent bool
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(generation), 0),
       COALESCE(bool_or(expires_at IS NULL AND revoked_at IS NULL), FALSE)
FROM porter_control_plane.agent_node_credentials
WHERE node_id = $1`, nodeID).Scan(&generation, &hasCurrent); err != nil {
		return NodeCredentialIssue{}, fmt.Errorf("read node credential generation: %w", err)
	}
	if generation == math.MaxInt64 {
		return NodeCredentialIssue{}, fmt.Errorf("%w: node credential generation exhausted", ErrFailedPrecondition)
	}

	var previousValidUntil *time.Time
	if hasCurrent {
		until := now.UTC().Add(rotationGrace)
		previousValidUntil = &until
		updated, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.agent_node_credentials
SET expires_at = $2
WHERE node_id = $1 AND expires_at IS NULL AND revoked_at IS NULL`, nodeID, until)
		if err != nil {
			return NodeCredentialIssue{}, fmt.Errorf("expire previous node credential: %w", err)
		}
		if err := requireOneRow(updated, "current node credential", nodeID); err != nil {
			return NodeCredentialIssue{}, err
		}
	}
	newGeneration := generation + 1
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.agent_node_credentials (node_id, generation, token_hash, issued_at)
VALUES ($1, $2, $3, $4)`, nodeID, newGeneration, tokenHash[:], now.UTC()); err != nil {
		return NodeCredentialIssue{}, fmt.Errorf("insert hashed node credential: %w", err)
	}
	eventType := "agent.credential.provisioned"
	if hasCurrent {
		eventType = "agent.credential.rotated"
	}
	if err := s.appendEvent(ctx, tx, eventType, eventRefs{fleetNodeID: nodeID}, map[string]any{
		"generation": newGeneration, "previous_valid_until": previousValidUntil,
	}); err != nil {
		return NodeCredentialIssue{}, err
	}
	if err := tx.Commit(); err != nil {
		return NodeCredentialIssue{}, fmt.Errorf("commit node credential provisioning: %w", err)
	}
	return NodeCredentialIssue{
		NodeID: nodeID, Token: token, Generation: newGeneration, PreviousValidUntil: previousValidUntil,
	}, nil
}

// AuthenticateNodeCredential checks a high-entropy bearer token against active
// stored hashes. Current credentials have no expiry; rotated credentials expire
// at their persisted grace deadline. The plaintext token is never queried or stored.
func (s *Store) AuthenticateNodeCredential(ctx context.Context, nodeID, token string, now time.Time) (bool, error) {
	if err := validateOperationID("node", nodeID); err != nil {
		return false, err
	}
	if now.IsZero() {
		return false, fmt.Errorf("%w: current time is required", ErrInvalidArgument)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(token) != 43 || len(raw) != 32 {
		return false, nil
	}
	presentedHash := sha256.Sum256([]byte(token))
	rows, err := s.db.QueryContext(ctx, `
SELECT token_hash FROM porter_control_plane.agent_node_credentials
WHERE node_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`, nodeID, now.UTC())
	if err != nil {
		return false, fmt.Errorf("read active node credential hashes: %w", err)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var storedHash []byte
		if err := rows.Scan(&storedHash); err != nil {
			return false, fmt.Errorf("scan node credential hash: %w", err)
		}
		if len(storedHash) == len(presentedHash) {
			matched |= subtle.ConstantTimeCompare(storedHash, presentedHash[:])
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read node credential hashes: %w", err)
	}
	return matched == 1, nil
}
