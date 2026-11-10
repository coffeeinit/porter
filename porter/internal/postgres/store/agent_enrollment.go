package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

const (
	defaultAgentEnrollmentTTL = 10 * time.Minute
	minAgentEnrollmentTTL     = time.Minute
	maxAgentEnrollmentTTL     = 30 * time.Minute
	enrollmentTokenPrefix     = "ptenr_"
)

// CreateAgentEnrollmentToken issues a short-lived secret once. PostgreSQL stores
// only its SHA-256 hash; the URL and operator-selected topology are immutable.
func (s *Store) CreateAgentEnrollmentToken(ctx context.Context, target v1.AgentEnrollmentTarget, controlPlaneURL string, now time.Time) (v1.AgentEnrollmentTokenIssue, error) {
	return s.CreateAgentEnrollmentTokenAt(ctx, target, controlPlaneURL, now, defaultAgentEnrollmentTTL)
}

// CreateAgentEnrollmentTokenAt permits deterministic time/TTL tests while
// enforcing the same one-to-thirty-minute bounds as production issuance.
func (s *Store) CreateAgentEnrollmentTokenAt(ctx context.Context, target v1.AgentEnrollmentTarget, controlPlaneURL string, now time.Time, ttl time.Duration) (v1.AgentEnrollmentTokenIssue, error) {
	if target != v1.AgentEnrollmentSameHost && target != v1.AgentEnrollmentRemoteHost {
		return v1.AgentEnrollmentTokenIssue{}, fmt.Errorf("%w: unsupported enrollment target", ErrInvalidArgument)
	}
	canonicalURL, err := validateEnrollmentControlPlaneURL(controlPlaneURL)
	if err != nil {
		return v1.AgentEnrollmentTokenIssue{}, err
	}
	if now.IsZero() || ttl < minAgentEnrollmentTTL || ttl > maxAgentEnrollmentTTL {
		return v1.AgentEnrollmentTokenIssue{}, fmt.Errorf("%w: enrollment token expiry must be between one and thirty minutes", ErrInvalidArgument)
	}
	id, err := newID("enr")
	if err != nil {
		return v1.AgentEnrollmentTokenIssue{}, err
	}
	token, tokenHash, err := newEnrollmentToken()
	if err != nil {
		return v1.AgentEnrollmentTokenIssue{}, err
	}
	now = now.UTC()
	expiresAt := now.Add(ttl)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.AgentEnrollmentTokenIssue{}, fmt.Errorf("begin agent enrollment token issue: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.agent_enrollment_tokens
    (id, token_hash, enrollment_target, control_plane_url, issued_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)`, id, tokenHash[:], target, canonicalURL, now, expiresAt); err != nil {
		return v1.AgentEnrollmentTokenIssue{}, fmt.Errorf("persist hashed agent enrollment token: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "agent.enrollment_token.issued", eventRefs{}, map[string]any{
		"enrollment_token_id": id,
		"enrollment_target":   target,
		"control_plane_url":   canonicalURL,
		"expires_at":          expiresAt,
	}); err != nil {
		return v1.AgentEnrollmentTokenIssue{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.AgentEnrollmentTokenIssue{}, fmt.Errorf("commit agent enrollment token issue: %w", err)
	}
	return v1.AgentEnrollmentTokenIssue{
		ID: id, Token: token, Target: target, ControlPlaneURL: canonicalURL, ExpiresAt: expiresAt,
	}, nil
}

// RedeemAgentEnrollmentToken atomically consumes a valid one-time token, creates
// a server-assigned offline Fleet node, stores only the node-token hash, and
// appends an audit event. Replay, expiry, and unknown tokens share one error.
func (s *Store) RedeemAgentEnrollmentToken(ctx context.Context, token string, request protocol.AgentEnrollmentRequestV2, now time.Time) (protocol.AgentEnrollmentResponseV2, error) {
	if !validEnrollmentToken(token) {
		return protocol.AgentEnrollmentResponseV2{}, ErrEnrollmentTokenUnavailable
	}
	if err := request.Validate(); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("%w: invalid agent enrollment request", ErrInvalidArgument)
	}
	if now.IsZero() {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("%w: current time is required", ErrInvalidArgument)
	}

	nodeID, err := newID("node")
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	nodeToken, nodeTokenHash, err := newNodeCredential()
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	now = now.UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("begin agent enrollment redemption: %w", err)
	}
	defer tx.Rollback()
	var enrollmentID string
	var target v1.AgentEnrollmentTarget
	var controlPlaneURL string
	if err := tx.QueryRowContext(ctx, `
SELECT id, enrollment_target, control_plane_url
FROM porter_control_plane.agent_enrollment_tokens
WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > $2
FOR UPDATE`, tokenHash[:], now).Scan(&enrollmentID, &target, &controlPlaneURL); err != nil {
		if err == sql.ErrNoRows {
			return protocol.AgentEnrollmentResponseV2{}, ErrEnrollmentTokenUnavailable
		}
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("read agent enrollment token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.fleet_nodes (id, name, state, enrollment_target)
VALUES ($1, $2, $3, $4)`, nodeID, request.Hostname, v1.FleetNodeOffline, target); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("create enrolled Fleet node: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.agent_node_credentials (node_id, generation, token_hash, issued_at)
VALUES ($1, 1, $2, $3)`, nodeID, nodeTokenHash[:], now); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("persist hashed enrolled node credential: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.agent_enrollment_tokens
SET consumed_at = $2, fleet_node_id = $3
WHERE id = $1 AND consumed_at IS NULL AND expires_at > $2`, enrollmentID, now, nodeID)
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("consume agent enrollment token: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("read agent enrollment token update count: %w", err)
	}
	if count != 1 {
		return protocol.AgentEnrollmentResponseV2{}, ErrEnrollmentTokenUnavailable
	}
	if err := s.appendEvent(ctx, tx, "fleet_node.enrolled", eventRefs{fleetNodeID: nodeID}, map[string]any{
		"name":                  request.Hostname,
		"state":                 v1.FleetNodeOffline,
		"enrollment_target":     target,
		"enrollment_token_id":   enrollmentID,
		"control_plane_url":     controlPlaneURL,
		"credential_generation": 1,
	}); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.AgentEnrollmentResponseV2{}, fmt.Errorf("commit agent enrollment redemption: %w", err)
	}
	return protocol.AgentEnrollmentResponseV2{
		ProtocolVersion:      protocol.VersionV2,
		NodeID:               nodeID,
		NodeToken:            nodeToken,
		EnrollmentTarget:     string(target),
		ControlPlaneURL:      controlPlaneURL,
		CredentialGeneration: 1,
		State:                string(v1.FleetNodeOffline),
	}, nil
}

func newEnrollmentToken() (string, [32]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate agent enrollment token: %w", err)
	}
	token := enrollmentTokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	return token, sha256.Sum256([]byte(token)), nil
}

func validEnrollmentToken(token string) bool {
	if !strings.HasPrefix(token, enrollmentTokenPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(token, enrollmentTokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(encoded) == 43 && len(raw) == 32
}

func newNodeCredential() (string, [32]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate enrolled node credential: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	return token, sha256.Sum256([]byte(token)), nil
}

func validateEnrollmentControlPlaneURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%w: control_plane_url must be an HTTPS origin without userinfo, path, query, or fragment", ErrInvalidArgument)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}
