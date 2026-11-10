package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

func (s *Store) CreateRelease(ctx context.Context, serviceID string, request v1.CreateReleaseRequest) (v1.Release, error) {
	request.Source.Repository = strings.TrimSpace(request.Source.Repository)
	request.Source.Ref = strings.TrimSpace(request.Source.Ref)
	if request.Source.Kind != v1.SourceGit || request.Source.Repository == "" || request.Source.Ref == "" {
		return v1.Release{}, fmt.Errorf("%w: Git source kind, repository, and ref are required", ErrInvalidArgument)
	}
	if request.Intent == "" {
		request.Intent = v1.ReleaseIntentPreview
	}
	if request.Intent != v1.ReleaseIntentPreview && request.Intent != v1.ReleaseIntentProduction {
		return v1.Release{}, fmt.Errorf("%w: release intent must be preview or production", ErrInvalidArgument)
	}
	if err := validateBuildSpec(&request.Build); err != nil {
		return v1.Release{}, err
	}
	buildJSON, err := json.Marshal(request.Build)
	if err != nil {
		return v1.Release{}, fmt.Errorf("encode release build spec: %w", err)
	}
	id, err := newID("rel")
	if err != nil {
		return v1.Release{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Release{}, fmt.Errorf("begin release creation: %w", err)
	}
	defer tx.Rollback()
	var serviceState v1.ServiceState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, serviceID).Scan(&serviceState); err != nil {
		return v1.Release{}, notFound("service", serviceID, err)
	}
	if serviceState != v1.ServiceActive {
		return v1.Release{}, fmt.Errorf("%w: service %q is %q", ErrFailedPrecondition, serviceID, serviceState)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(sequence), 0) + 1
FROM porter_control_plane.releases WHERE service_id = $1`, serviceID).Scan(&sequence); err != nil {
		return v1.Release{}, fmt.Errorf("allocate release sequence: %w", err)
	}

	release, err := scanRelease(tx.QueryRowContext(ctx, `
	INSERT INTO porter_control_plane.releases
	    (id, service_id, sequence, state, source_kind, source_repository, source_ref, build_spec, deployment_intent)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	RETURNING id, service_id, sequence, state, source_kind, source_repository, source_ref,
	          resolved_commit, build_spec, artifact_digest, guest_ready_digest, guest_ready_at,
	          health_gate_status, health_gate_checked_at, health_gate_error, failure_message, created_at, updated_at, deployment_intent`,
		id, serviceID, sequence, v1.ReleaseQueued, request.Source.Kind, request.Source.Repository,
		request.Source.Ref, string(buildJSON), request.Intent))
	if err != nil {
		return v1.Release{}, fmt.Errorf("create release: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.created", eventRefs{serviceID: serviceID, releaseID: release.ID}, map[string]any{
		"sequence": release.Sequence, "state": release.State,
	}); err != nil {
		return v1.Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Release{}, fmt.Errorf("commit release creation: %w", err)
	}
	return release, nil
}

func validateBuildSpec(build *v1.BuildSpec) error {
	switch build.Kind {
	case v1.BuildDockerfile:
		build.DockerfilePath = strings.TrimSpace(build.DockerfilePath)
		if build.DockerfilePath == "" {
			return fmt.Errorf("%w: dockerfile_path is required", ErrInvalidArgument)
		}
	case v1.BuildCompose:
		build.ComposeFile = strings.TrimSpace(build.ComposeFile)
		build.PrimaryService = strings.TrimSpace(build.PrimaryService)
		if build.ComposeFile == "" || build.PrimaryService == "" {
			return fmt.Errorf("%w: compose_file and primary_service are required", ErrInvalidArgument)
		}
	default:
		return fmt.Errorf("%w: unsupported build kind %q", ErrInvalidArgument, build.Kind)
	}
	return nil
}

func (s *Store) GetRelease(ctx context.Context, id string) (v1.Release, error) {
	release, err := scanRelease(s.db.QueryRowContext(ctx, releaseSelect+` WHERE id = $1`, id))
	if err != nil {
		return v1.Release{}, notFound("release", id, err)
	}
	return release, nil
}

func (s *Store) ListReleases(ctx context.Context, serviceID string) ([]v1.Release, error) {
	rows, err := s.db.QueryContext(ctx, releaseSelect+` WHERE service_id = $1 ORDER BY sequence DESC, id`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	releases := make([]v1.Release, 0)
	for rows.Next() {
		release, err := scanRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate releases: %w", err)
	}
	return releases, nil
}

const releaseSelect = `
SELECT id, service_id, sequence, state, source_kind, source_repository, source_ref,
       resolved_commit, build_spec, artifact_digest, guest_ready_digest, guest_ready_at,
       health_gate_status, health_gate_checked_at, health_gate_error, failure_message, created_at, updated_at,
       deployment_intent
FROM porter_control_plane.releases`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRelease(row rowScanner) (v1.Release, error) {
	var release v1.Release
	var sourceKind string
	var resolvedCommit, artifactDigest, guestReadyDigest sql.NullString
	var guestReadyAt, healthGateCheckedAt sql.NullTime
	var healthGateStatus string
	var intent string
	var buildJSON []byte
	err := row.Scan(&release.ID, &release.ServiceID, &release.Sequence, &release.State,
		&sourceKind, &release.Source.Repository, &release.Source.Ref, &resolvedCommit,
		&buildJSON, &artifactDigest, &guestReadyDigest, &guestReadyAt,
		&healthGateStatus, &healthGateCheckedAt, &release.HealthGateError,
		&release.FailureMessage, &release.CreatedAt, &release.UpdatedAt, &intent)
	if err != nil {
		return v1.Release{}, err
	}
	release.Intent = v1.ReleaseIntent(intent)
	release.Source.Kind = v1.SourceKind(sourceKind)
	if resolvedCommit.Valid {
		release.ResolvedCommit = resolvedCommit.String
	}
	if artifactDigest.Valid {
		release.ArtifactDigest = artifactDigest.String
	}
	release.GuestReady = guestReadyDigest.Valid && guestReadyDigest.String == release.ArtifactDigest
	if guestReadyAt.Valid {
		value := guestReadyAt.Time
		release.GuestReadyAt = &value
	}
	release.HealthGate = v1.ReleaseHealthGateStatus(healthGateStatus)
	if healthGateCheckedAt.Valid {
		value := healthGateCheckedAt.Time
		release.HealthCheckedAt = &value
	}
	if err := json.Unmarshal(buildJSON, &release.Build); err != nil {
		return v1.Release{}, fmt.Errorf("decode release build spec: %w", err)
	}
	return release, nil
}

func (s *Store) PinReleaseCommit(ctx context.Context, id, commit string) error {
	if !validCommit(commit) {
		return fmt.Errorf("%w: resolved commit must be a lowercase 40- or 64-character Git object ID", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release commit pin: %w", err)
	}
	defer tx.Rollback()
	var state v1.ReleaseState
	var pinnedCommit sql.NullString
	if err := tx.QueryRowContext(ctx, `
	SELECT state, resolved_commit FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&state, &pinnedCommit); err != nil {
		return notFound("release", id, err)
	}
	if state != v1.ReleaseQueued && state != v1.ReleaseBuilding {
		return fmt.Errorf("%w: cannot pin source commit while release is %q", ErrFailedPrecondition, state)
	}
	if pinnedCommit.Valid {
		if pinnedCommit.String == commit {
			return nil
		}
		return fmt.Errorf("%w: resolved source commit is already pinned", ErrFailedPrecondition)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.releases
SET resolved_commit = $1, updated_at = now() WHERE id = $2`, commit, id); err != nil {
		return fmt.Errorf("pin release commit: %w", err)
	}
	var serviceID string
	if err := tx.QueryRowContext(ctx, `SELECT service_id FROM porter_control_plane.releases WHERE id = $1`, id).Scan(&serviceID); err != nil {
		return fmt.Errorf("read release service after pin: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.commit_pinned", eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{"pinned": true}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release source pin: %w", err)
	}
	return nil
}

func (s *Store) SetReleaseArtifactDigest(ctx context.Context, id, digest string) error {
	if !validArtifactDigest(digest) {
		return fmt.Errorf("%w: artifact digest must be sha256 followed by 64 lowercase hex characters", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release artifact update: %w", err)
	}
	defer tx.Rollback()
	var state v1.ReleaseState
	var existingDigest sql.NullString
	if err := tx.QueryRowContext(ctx, `
	SELECT state, artifact_digest FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&state, &existingDigest); err != nil {
		return notFound("release", id, err)
	}
	if state != v1.ReleaseBuilding && state != v1.ReleaseReady {
		return fmt.Errorf("%w: cannot set artifact digest while release is %q", ErrFailedPrecondition, state)
	}
	if existingDigest.Valid {
		if existingDigest.String == digest {
			return nil
		}
		return fmt.Errorf("%w: artifact digest is already pinned", ErrFailedPrecondition)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.releases
SET artifact_digest = $1, updated_at = now() WHERE id = $2`, digest, id); err != nil {
		return fmt.Errorf("set release artifact digest: %w", err)
	}
	var serviceID string
	if err := tx.QueryRowContext(ctx, `SELECT service_id FROM porter_control_plane.releases WHERE id = $1`, id).Scan(&serviceID); err != nil {
		return fmt.Errorf("read release service after artifact pin: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.artifact_digest_set", eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{"pinned": true}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release artifact digest: %w", err)
	}
	return nil
}

// MarkReleaseGuestReady records an immutable agent receipt after the digest has
// been verified by booting it through the agent's local Firecracker runtime.
func (s *Store) MarkReleaseGuestReady(ctx context.Context, id, digest string) error {
	if !validArtifactDigest(digest) {
		return fmt.Errorf("%w: guest-ready digest must be sha256 followed by 64 lowercase hex characters", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release guest-ready update: %w", err)
	}
	defer tx.Rollback()
	var state v1.ReleaseState
	var artifactDigest, guestReadyDigest sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT state, artifact_digest, guest_ready_digest
		FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&state, &artifactDigest, &guestReadyDigest); err != nil {
		return notFound("release", id, err)
	}
	if state != v1.ReleaseBuilding {
		return fmt.Errorf("%w: guest readiness can be recorded only while release is building", ErrFailedPrecondition)
	}
	if !artifactDigest.Valid || artifactDigest.String != digest {
		return fmt.Errorf("%w: guest-ready receipt does not match the pinned artifact digest", ErrFailedPrecondition)
	}
	if guestReadyDigest.Valid {
		if guestReadyDigest.String == digest {
			return nil
		}
		return fmt.Errorf("%w: guest-ready artifact digest is already pinned", ErrFailedPrecondition)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE porter_control_plane.releases
		SET guest_ready_digest = $1, guest_ready_at = now(), updated_at = now()
		WHERE id = $2`, digest, id); err != nil {
		return fmt.Errorf("mark release guest-ready: %w", err)
	}
	var serviceID string
	if err := tx.QueryRowContext(ctx, `SELECT service_id FROM porter_control_plane.releases WHERE id = $1`, id).Scan(&serviceID); err != nil {
		return fmt.Errorf("read release service after guest-ready receipt: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.guest_ready", eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{"digest": digest}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release guest-ready receipt: %w", err)
	}
	return nil
}

// RecordReleaseHealthGate records the result of the deployment health check.
// It accepts no caller-provided error text, preventing secrets and host paths
// from being persisted or returned through the release API.
func (s *Store) RecordReleaseHealthGate(ctx context.Context, id, digest string, passed bool) error {
	if !validArtifactDigest(digest) {
		return fmt.Errorf("%w: health-gate digest must be sha256 followed by 64 lowercase hex characters", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release health-gate update: %w", err)
	}
	defer tx.Rollback()
	var state v1.ReleaseState
	var artifactDigest, guestReadyDigest, healthDigest sql.NullString
	var healthStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT state, artifact_digest, guest_ready_digest, health_gate_status, health_gate_digest
		FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&state, &artifactDigest, &guestReadyDigest, &healthStatus, &healthDigest); err != nil {
		return notFound("release", id, err)
	}
	if state != v1.ReleaseDeploying {
		return fmt.Errorf("%w: health gate can be recorded only while release is deploying", ErrFailedPrecondition)
	}
	if !artifactDigest.Valid || artifactDigest.String != digest || !guestReadyDigest.Valid || guestReadyDigest.String != digest {
		return fmt.Errorf("%w: health-gate result does not match the guest-ready artifact", ErrFailedPrecondition)
	}
	status := v1.ReleaseHealthGateFailed
	errorText := "health check failed"
	if passed {
		status = v1.ReleaseHealthGatePassed
		errorText = ""
	}
	if healthStatus == string(status) && healthDigest.Valid && healthDigest.String == digest {
		return nil
	}
	if healthStatus != string(v1.ReleaseHealthGatePending) {
		return fmt.Errorf("%w: health gate must be pending before a result is recorded", ErrFailedPrecondition)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE porter_control_plane.releases
		SET health_gate_status = $1, health_gate_digest = $2, health_gate_checked_at = now(),
		    health_gate_error = $3, updated_at = now()
		WHERE id = $4`, status, digest, errorText, id); err != nil {
		return fmt.Errorf("record release health-gate result: %w", err)
	}
	var serviceID string
	if err := tx.QueryRowContext(ctx, `SELECT service_id FROM porter_control_plane.releases WHERE id = $1`, id).Scan(&serviceID); err != nil {
		return fmt.Errorf("read release service after health-gate result: %w", err)
	}
	eventType := "release.health_gate_failed"
	if passed {
		eventType = "release.health_gate_passed"
	}
	if err := s.appendEvent(ctx, tx, eventType, eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{"digest": digest}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release health-gate result: %w", err)
	}
	return nil
}

func (s *Store) SetReleaseFailureMessage(ctx context.Context, id, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release failure-detail update: %w", err)
	}
	defer tx.Rollback()
	var previous sql.NullString
	var serviceID string
	if err := tx.QueryRowContext(ctx, `
SELECT service_id, failure_message FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&serviceID, &previous); err != nil {
		return notFound("release", id, err)
	}
	if previous.Valid && previous.String == message || !previous.Valid && message == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE porter_control_plane.releases SET failure_message = $1, updated_at = now() WHERE id = $2`, message, id); err != nil {
		return fmt.Errorf("set release failure message: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "release.failure_detail_changed", eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{"present": message != ""}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release failure-detail update: %w", err)
	}
	return nil
}

// TransitionRelease applies the target lifecycle state. Activation and
// superseding of the prior active release are one transaction; no VM or build
// operation is started by this persistence method.
func (s *Store) TransitionRelease(ctx context.Context, id string, target v1.ReleaseState) error {
	if !validReleaseState(target) {
		return fmt.Errorf("%w: unsupported release state %q", ErrInvalidArgument, target)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release transition: %w", err)
	}
	defer tx.Rollback()

	var serviceID string
	if err := tx.QueryRowContext(ctx, `
SELECT service_id FROM porter_control_plane.releases WHERE id = $1`, id).Scan(&serviceID); err != nil {
		return notFound("release", id, err)
	}
	var serviceState v1.ServiceState
	if err := tx.QueryRowContext(ctx, `
	SELECT state FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, serviceID).Scan(&serviceState); err != nil {
		return notFound("service", serviceID, err)
	}
	if target == v1.ReleaseActive && serviceState != v1.ServiceActive {
		return fmt.Errorf("%w: cannot activate a release while service %q is %q", ErrFailedPrecondition, serviceID, serviceState)
	}
	var current v1.ReleaseState
	var artifactDigest, guestReadyDigest, healthDigest sql.NullString
	var healthStatus string
	if err := tx.QueryRowContext(ctx, `
	SELECT state, artifact_digest, guest_ready_digest, health_gate_status, health_gate_digest
	FROM porter_control_plane.releases WHERE id = $1 FOR UPDATE`, id).Scan(&current, &artifactDigest, &guestReadyDigest, &healthStatus, &healthDigest); err != nil {
		return notFound("release", id, err)
	}
	if !canTransitionRelease(current, target) {
		return fmt.Errorf("%w: release cannot transition from %q to %q", ErrFailedPrecondition, current, target)
	}
	if target == v1.ReleaseReady || target == v1.ReleaseDeploying {
		if !artifactDigest.Valid || !guestReadyDigest.Valid || artifactDigest.String != guestReadyDigest.String {
			return fmt.Errorf("%w: release artifact must be guest-ready before %q", ErrFailedPrecondition, target)
		}
	}
	if target == v1.ReleaseActive {
		if !artifactDigest.Valid || !guestReadyDigest.Valid || artifactDigest.String != guestReadyDigest.String ||
			healthStatus != string(v1.ReleaseHealthGatePassed) || !healthDigest.Valid || healthDigest.String != artifactDigest.String {
			return fmt.Errorf("%w: release must be guest-ready and pass its health gate before activation", ErrFailedPrecondition)
		}
	}
	if target == v1.ReleaseActive {
		rows, err := tx.QueryContext(ctx, `
UPDATE porter_control_plane.releases
SET state = 'superseded', updated_at = now()
	WHERE service_id = $1 AND state = 'active' AND id <> $2
	RETURNING id`, serviceID, id)
		if err != nil {
			return fmt.Errorf("supersede active release: %w", err)
		}
		var supersededIDs []string
		for rows.Next() {
			var supersededID string
			if err := rows.Scan(&supersededID); err != nil {
				rows.Close()
				return fmt.Errorf("scan superseded release: %w", err)
			}
			supersededIDs = append(supersededIDs, supersededID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate superseded releases: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close superseded release rows: %w", err)
		}
		for _, supersededID := range supersededIDs {
			if err := s.appendEvent(ctx, tx, "release.state_changed", eventRefs{serviceID: serviceID, releaseID: supersededID}, map[string]any{
				"from": v1.ReleaseActive, "to": v1.ReleaseSuperseded,
			}); err != nil {
				return err
			}
		}
	}
	var updateErr error
	if target == v1.ReleaseDeploying {
		_, updateErr = tx.ExecContext(ctx, `
	UPDATE porter_control_plane.releases
	SET state = $1, health_gate_status = 'pending', updated_at = now() WHERE id = $2`, target, id)
	} else {
		_, updateErr = tx.ExecContext(ctx, `
	UPDATE porter_control_plane.releases
	SET state = $1, updated_at = now() WHERE id = $2`, target, id)
	}
	if updateErr != nil {
		return fmt.Errorf("transition release: %w", updateErr)
	}
	if err := s.appendEvent(ctx, tx, "release.state_changed", eventRefs{serviceID: serviceID, releaseID: id}, map[string]any{
		"from": current, "to": target,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release transition: %w", err)
	}
	return nil
}

func validReleaseState(state v1.ReleaseState) bool {
	switch state {
	case v1.ReleaseQueued, v1.ReleaseBlocked, v1.ReleaseBuilding, v1.ReleaseReady, v1.ReleaseDeploying,
		v1.ReleaseActive, v1.ReleaseFailed, v1.ReleaseSuperseded:
		return true
	default:
		return false
	}
}

func canTransitionRelease(current, target v1.ReleaseState) bool {
	switch current {
	case v1.ReleaseQueued:
		return target == v1.ReleaseBlocked || target == v1.ReleaseBuilding || target == v1.ReleaseFailed
	case v1.ReleaseBlocked:
		return target == v1.ReleaseBuilding || target == v1.ReleaseFailed
	case v1.ReleaseBuilding:
		return target == v1.ReleaseReady || target == v1.ReleaseFailed
	case v1.ReleaseReady:
		return target == v1.ReleaseDeploying || target == v1.ReleaseFailed
	case v1.ReleaseDeploying:
		return target == v1.ReleaseActive || target == v1.ReleaseFailed
	case v1.ReleaseActive:
		return target == v1.ReleaseSuperseded
	default:
		return false
	}
}
