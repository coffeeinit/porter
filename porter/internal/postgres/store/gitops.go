package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

// CreateBlockedReleaseIntent records the requested source and environment in one
// transaction with a blocked operation. It never resolves Git refs, builds an
// artifact, or queues an agent command.
func (s *Store) CreateBlockedReleaseIntent(ctx context.Context, serviceID string, request v1.CreateReleaseRequest, idempotencyKey string) (v1.Release, v1.ReleaseOperation, error) {
	request.Source.Repository = strings.TrimSpace(request.Source.Repository)
	request.Source.Ref = strings.TrimSpace(request.Source.Ref)
	if request.Source.Kind != v1.SourceGit || request.Source.Repository == "" || request.Source.Ref == "" {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("%w: Git source kind, repository, and ref are required", ErrInvalidArgument)
	}
	if request.Intent == "" {
		request.Intent = v1.ReleaseIntentPreview
	}
	if request.Intent != v1.ReleaseIntentPreview && request.Intent != v1.ReleaseIntentProduction {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("%w: release intent must be preview or production", ErrInvalidArgument)
	}
	if err := validateBuildSpec(&request.Build); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}
	fingerprint, err := releaseIntentFingerprint(serviceID, request)
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}
	buildJSON, err := json.Marshal(request.Build)
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("encode release build spec: %w", err)
	}
	releaseID, err := newID("rel")
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}
	operationID, err := newID("rop")
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("begin release intent: %w", err)
	}
	defer tx.Rollback()
	var serviceState v1.ServiceState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, serviceID).Scan(&serviceState); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, notFound("service", serviceID, err)
	}
	if serviceState != v1.ServiceActive {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("%w: service %q is %q", ErrFailedPrecondition, serviceID, serviceState)
	}
	var existingID, existingFingerprint string
	err = tx.QueryRowContext(ctx, `
SELECT id, request_sha256 FROM porter_control_plane.release_operations
WHERE service_id = $1 AND idempotency_key = $2`, serviceID, idempotencyKey).Scan(&existingID, &existingFingerprint)
	if err == nil {
		if existingFingerprint != fingerprint {
			return v1.Release{}, v1.ReleaseOperation{}, ErrIdempotencyConflict
		}
		existingOperation, err := scanReleaseOperation(tx.QueryRowContext(ctx, releaseOperationSelect+` WHERE id = $1`, existingID))
		if err != nil {
			return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("read idempotent release operation: %w", err)
		}
		existingRelease, err := scanRelease(tx.QueryRowContext(ctx, releaseSelect+` WHERE id = $1`, existingOperation.ReleaseID))
		if err != nil {
			return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("read idempotent release: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("commit idempotent release read: %w", err)
		}
		return existingRelease, existingOperation, nil
	}
	if err != sql.ErrNoRows {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("check release intent idempotency key: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sequence), 0) + 1
FROM porter_control_plane.releases WHERE service_id = $1`, serviceID).Scan(&sequence); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("allocate release sequence: %w", err)
	}
	release, err := scanRelease(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.releases
    (id, service_id, sequence, state, source_kind, source_repository, source_ref, build_spec, deployment_intent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, service_id, sequence, state, source_kind, source_repository, source_ref,
          resolved_commit, build_spec, artifact_digest, guest_ready_digest, guest_ready_at,
          health_gate_status, health_gate_checked_at, health_gate_error, failure_message,
          created_at, updated_at, deployment_intent`,
		releaseID, serviceID, sequence, v1.ReleaseBlocked, request.Source.Kind,
		request.Source.Repository, request.Source.Ref, string(buildJSON), request.Intent))
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("create blocked release intent: %w", err)
	}
	operation, err := scanReleaseOperation(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.release_operations
    (id, service_id, release_id, kind, state, intent, blocker, requested_ref, checks, idempotency_key, request_sha256)
VALUES ($1, $2, $3, $4, 'blocked', $5, 'builder_unavailable', $6, '[]'::jsonb, $7, $8)
RETURNING id, service_id, release_id, kind, state, intent, blocker, requested_ref,
          resolved_commit, checks, created_at, updated_at`,
		operationID, serviceID, releaseID, v1.ReleaseOperationBuild, request.Intent,
		request.Source.Ref, idempotencyKey, fingerprint))
	if err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("create blocked release operation: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.intent_recorded", eventRefs{
		serviceID: serviceID, releaseID: releaseID, operationID: operationID,
	}, map[string]any{"sequence": release.Sequence, "intent": request.Intent, "state": v1.ReleaseBlocked, "blocker": "builder_unavailable"}); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Release{}, v1.ReleaseOperation{}, fmt.Errorf("commit blocked release intent: %w", err)
	}
	return release, operation, nil
}

// ListReleaseOperations returns the bounded append-only history for a release.
func (s *Store) ListReleaseOperations(ctx context.Context, releaseID string, limit int) ([]v1.ReleaseOperation, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, releaseOperationsWithBuilderSelect+`
WHERE o.release_id = $1 ORDER BY o.created_at DESC, o.id DESC LIMIT $2`, releaseID, limit)
	if err != nil {
		return nil, fmt.Errorf("list release operations: %w", err)
	}
	defer rows.Close()
	operations := make([]v1.ReleaseOperation, 0)
	for rows.Next() {
			operation, err := scanReleaseOperationWithBuilder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release operation: %w", err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release operations: %w", err)
	}
	return operations, nil
}

// RequestProductionPromotion records a check-only production promotion attempt.
// It always remains blocked until a real deployment/health worker is available.
func (s *Store) RequestProductionPromotion(ctx context.Context, releaseID, idempotencyKey string) (v1.ReleaseOperation, error) {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return v1.ReleaseOperation{}, err
	}
	fingerprint := requestFingerprint("production-promotion", releaseID)
	operationID, err := newID("rop")
	if err != nil {
		return v1.ReleaseOperation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.ReleaseOperation{}, fmt.Errorf("begin production promotion check: %w", err)
	}
	defer tx.Rollback()
	var serviceID, sourceRef string
	var releaseState v1.ReleaseState
	var resolvedCommit, artifactDigest, guestReadyDigest, healthDigest sql.NullString
	var healthStatus string
	if err := tx.QueryRowContext(ctx, `
SELECT r.service_id, r.state, r.source_ref, r.resolved_commit, r.artifact_digest,
       r.guest_ready_digest, r.health_gate_status, r.health_gate_digest
FROM porter_control_plane.releases r
WHERE r.id = $1 FOR UPDATE`, releaseID).Scan(
		&serviceID, &releaseState, &sourceRef, &resolvedCommit, &artifactDigest,
		&guestReadyDigest, &healthStatus, &healthDigest,
	); err != nil {
		return v1.ReleaseOperation{}, notFound("release", releaseID, err)
	}
	var existingID, existingFingerprint string
	err = tx.QueryRowContext(ctx, `
SELECT id, request_sha256 FROM porter_control_plane.release_operations
WHERE service_id = $1 AND idempotency_key = $2`, serviceID, idempotencyKey).Scan(&existingID, &existingFingerprint)
	if err == nil {
		if existingFingerprint != fingerprint {
			return v1.ReleaseOperation{}, ErrIdempotencyConflict
		}
		operation, err := scanReleaseOperation(tx.QueryRowContext(ctx, releaseOperationSelect+` WHERE id = $1`, existingID))
		if err != nil {
			return v1.ReleaseOperation{}, fmt.Errorf("read idempotent promotion operation: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return v1.ReleaseOperation{}, fmt.Errorf("commit idempotent promotion read: %w", err)
		}
		return operation, nil
	}
	if err != sql.ErrNoRows {
		return v1.ReleaseOperation{}, fmt.Errorf("check promotion idempotency key: %w", err)
	}
	checks := []v1.ReleasePromotionCheck{
		promotionCheck("immutable_commit_pinned", resolvedCommit.Valid && validCommit(resolvedCommit.String), "source ref has not been resolved and pinned by the isolated builder"),
		promotionCheck("artifact_digest_pinned", artifactDigest.Valid && validArtifactDigest(artifactDigest.String), "build artifact digest is not available"),
		promotionCheck("guest_ready_for_artifact", artifactDigest.Valid && guestReadyDigest.Valid && artifactDigest.String == guestReadyDigest.String, "matching Firecracker guest-ready receipt is not available"),
		promotionCheck("health_gate_passed_for_artifact", artifactDigest.Valid && healthStatus == string(v1.ReleaseHealthGatePassed) && healthDigest.Valid && artifactDigest.String == healthDigest.String, "matching production health-gate pass is not available"),
		promotionCheck("release_state_eligible", releaseState == v1.ReleaseReady || releaseState == v1.ReleaseDeploying || releaseState == v1.ReleaseActive, "release is not in a ready, deploying, or active state"),
		promotionCheck("production_activation_worker", false, "production deployment worker is not configured"),
	}
	blocker := "production_activation_unavailable"
	for _, check := range checks {
		if !check.Passed {
			blocker = check.Name
			break
		}
	}
	checksJSON, err := json.Marshal(checks)
	if err != nil {
		return v1.ReleaseOperation{}, fmt.Errorf("encode promotion checks: %w", err)
	}
	operation, err := scanReleaseOperation(tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.release_operations
    (id, service_id, release_id, kind, state, intent, blocker, requested_ref, resolved_commit, checks, idempotency_key, request_sha256)
VALUES ($1, $2, $3, $4, 'blocked', 'production', $5, $6, $7, $8::jsonb, $9, $10)
RETURNING id, service_id, release_id, kind, state, intent, blocker, requested_ref,
          resolved_commit, checks, created_at, updated_at`,
		operationID, serviceID, releaseID, v1.ReleaseOperationPromotion, blocker,
		sourceRef, nullString(resolvedCommit), string(checksJSON), idempotencyKey, fingerprint))
	if err != nil {
		return v1.ReleaseOperation{}, fmt.Errorf("persist blocked production promotion: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.production_promotion_blocked", eventRefs{
		serviceID: serviceID, releaseID: releaseID, operationID: operationID,
	}, map[string]any{"blocker": blocker, "checks": checks}); err != nil {
		return v1.ReleaseOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.ReleaseOperation{}, fmt.Errorf("commit blocked production promotion: %w", err)
	}
	return operation, nil
}

func promotionCheck(name string, passed bool, detail string) v1.ReleasePromotionCheck {
	if passed {
		detail = "verified"
	}
	return v1.ReleasePromotionCheck{Name: name, Passed: passed, Detail: detail}
}

func validateIdempotencyKey(key string) error {
	if key == "" || len(key) > 128 || strings.TrimSpace(key) != key {
		return fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", ErrInvalidArgument)
	}
	for _, character := range key {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && !strings.ContainsRune("-_.:", character) {
			return fmt.Errorf("%w: Idempotency-Key contains an unsupported character", ErrInvalidArgument)
		}
	}
	return nil
}

func releaseIntentFingerprint(serviceID string, request v1.CreateReleaseRequest) (string, error) {
	request.IdempotencyKey = ""
	encoded, err := json.Marshal(struct {
		ServiceID string                  `json:"service_id"`
		Request   v1.CreateReleaseRequest `json:"request"`
	}{serviceID, request})
	if err != nil {
		return "", fmt.Errorf("encode release intent fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func requestFingerprint(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func nullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

const releaseOperationSelect = `
SELECT id, service_id, release_id, kind, state, intent, blocker, requested_ref,
       resolved_commit, checks, created_at, updated_at
FROM porter_control_plane.release_operations`

func scanReleaseOperation(row rowScanner) (v1.ReleaseOperation, error) {
	var operation v1.ReleaseOperation
	var kind, state, intent string
	var resolvedCommit sql.NullString
	var checks []byte
	if err := row.Scan(&operation.ID, &operation.ServiceID, &operation.ReleaseID,
		&kind, &state, &intent, &operation.Blocker, &operation.RequestedRef,
		&resolvedCommit, &checks, &operation.CreatedAt, &operation.UpdatedAt); err != nil {
		return v1.ReleaseOperation{}, err
	}
	operation.Kind = v1.ReleaseOperationKind(kind)
	operation.State = v1.ReleaseOperationState(state)
	operation.Intent = v1.ReleaseIntent(intent)
	if resolvedCommit.Valid {
		operation.ResolvedCommit = resolvedCommit.String
	}
	if len(checks) > 0 {
		if err := json.Unmarshal(checks, &operation.Checks); err != nil {
			return v1.ReleaseOperation{}, fmt.Errorf("decode release operation checks: %w", err)
		}
	}
	return operation, nil
}
