package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

// RecordReleaseBuilderCommandResult persists an authenticated builder result
// against the exact pinned, running release operation. It records the OCI
// receipt only; it never marks a release artifact guest-ready or deploys it.
func (s *Store) RecordReleaseBuilderCommandResult(ctx context.Context, spec protocol.BuilderBuildSpec, result protocol.CommandResult) error {
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("%w: invalid builder spec: %v", ErrInvalidArgument, err)
	}
	if !validBuilderReceiptID(result.CommandID) || !validBuilderReceiptID(result.OperationID) || !validBuilderReceiptID(result.NodeID) || result.Attempt < 1 {
		return fmt.Errorf("%w: builder command, operation, node IDs, and positive attempt are required", ErrInvalidArgument)
	}
	if result.Success {
		if result.State != "built" || result.Error != "" || result.BuilderResult == nil {
			return fmt.Errorf("%w: successful builder result requires state=built and typed builder_result without an error", ErrInvalidArgument)
		}
		if err := result.BuilderResult.ValidateFor(spec); err != nil {
			return fmt.Errorf("%w: builder result does not match its request: %v", ErrInvalidArgument, err)
		}
	} else {
		if result.BuilderResult != nil || strings.TrimSpace(result.Error) == "" || len(result.Error) > maxCommandResultError || (result.State != "" && result.State != "failed") {
			return fmt.Errorf("%w: failed builder result requires a bounded error and no builder_result", ErrInvalidArgument)
		}
	}

	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode pinned builder spec: %w", err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode builder command result: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release builder result: %w", err)
	}
	defer tx.Rollback()

	var serviceID, releaseID, operationKind, operationState, sourceRepository, releaseState string
	var operationCommit, releaseCommit sql.NullString
	var storedSpecJSON []byte
	err = tx.QueryRowContext(ctx, `
SELECT o.service_id, o.release_id, o.kind, o.state, o.resolved_commit, o.builder_spec,
       r.source_repository, r.resolved_commit, r.state
FROM porter_control_plane.release_operations o
JOIN porter_control_plane.releases r
  ON r.id = o.release_id AND r.service_id = o.service_id
WHERE o.id = $1
FOR UPDATE OF o`, result.OperationID).Scan(
		&serviceID, &releaseID, &operationKind, &operationState, &operationCommit, &storedSpecJSON,
		&sourceRepository, &releaseCommit, &releaseState,
	)
	if err != nil {
		return notFound("release operation", result.OperationID, err)
	}
	if operationKind != string(v1.ReleaseOperationBuild) {
		return fmt.Errorf("%w: builder result can be recorded only for a build operation", ErrFailedPrecondition)
	}
	if operationState != string(v1.ReleaseOperationRunning) {
		return fmt.Errorf("%w: builder result requires a running release operation", ErrCommandStateConflict)
	}
	if releaseState != string(v1.ReleaseBuilding) {
		return fmt.Errorf("%w: builder result requires a building release", ErrFailedPrecondition)
	}
	if len(storedSpecJSON) == 0 || !operationCommit.Valid || !releaseCommit.Valid {
		return fmt.Errorf("%w: builder operation has no persisted pinned spec and commit", ErrFailedPrecondition)
	}
	var storedSpec protocol.BuilderBuildSpec
	if err := json.Unmarshal(storedSpecJSON, &storedSpec); err != nil {
		return fmt.Errorf("decode persisted builder spec: %w", err)
	}
	if err := storedSpec.Validate(); err != nil {
		return fmt.Errorf("%w: persisted builder spec is invalid", ErrFailedPrecondition)
	}
	if storedSpec != spec || operationCommit.String != spec.Commit || releaseCommit.String != spec.Commit || sourceRepository != spec.RepositoryURL {
		return fmt.Errorf("%w: builder result does not match the exact pinned release operation", ErrFailedPrecondition)
	}

	var identical bool
	err = tx.QueryRowContext(ctx, `
SELECT builder_spec = $3::jsonb AND command_result = $4::jsonb
FROM porter_control_plane.release_builder_results
WHERE command_id = $1 OR (operation_id = $2 AND attempt = $5)
LIMIT 1`, result.CommandID, result.OperationID, string(specJSON), string(resultJSON), result.Attempt).Scan(&identical)
	if err == nil {
		if !identical {
			return ErrCommandResultConflict
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit idempotent builder result read: %w", err)
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check builder result idempotency: %w", err)
	}

	receiptID, err := newID("brs")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.release_builder_results
    (id, command_id, operation_id, service_id, release_id, node_id, attempt,
     builder_spec, command_result, success, error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, $11)`,
		receiptID, result.CommandID, result.OperationID, serviceID, releaseID, result.NodeID, result.Attempt,
		string(specJSON), string(resultJSON), result.Success, result.Error); err != nil {
		return fmt.Errorf("insert append-only release builder result: %w", err)
	}
	data := map[string]any{
		"build_id": spec.BuildID, "commit": spec.Commit, "attempt": result.Attempt, "success": result.Success,
	}
	if result.BuilderResult != nil {
		data["digest"] = result.BuilderResult.Digest
	}
	if err := s.appendEvent(ctx, tx, "release.builder_result_recorded", eventRefs{
		serviceID: serviceID, releaseID: releaseID, operationID: result.OperationID, fleetNodeID: result.NodeID,
	}, data); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release builder result: %w", err)
	}
	return nil
}

const releaseOperationsWithBuilderSelect = `
SELECT o.id, o.service_id, o.release_id, o.kind,
       CASE WHEN b.command_id IS NULL THEN o.state WHEN b.success THEN 'succeeded' ELSE 'failed' END,
       o.intent,
       CASE WHEN b.command_id IS NULL THEN o.blocker WHEN b.success THEN '' ELSE 'builder_failed' END,
       o.requested_ref, o.resolved_commit, o.checks, o.created_at,
       COALESCE(b.received_at, o.updated_at),
       b.command_id, b.success, b.attempt, b.command_result, b.received_at
FROM porter_control_plane.release_operations o
LEFT JOIN LATERAL (
    SELECT command_id, success, attempt, command_result, received_at
    FROM porter_control_plane.release_builder_results
    WHERE operation_id = o.id
    ORDER BY attempt DESC, received_at DESC
    LIMIT 1
) b ON TRUE`

func scanReleaseOperationWithBuilder(row rowScanner) (v1.ReleaseOperation, error) {
	var operation v1.ReleaseOperation
	var kind, state, intent string
	var resolvedCommit, builderCommand sql.NullString
	var checks, commandJSON []byte
	var builderSuccess sql.NullBool
	var builderAttempt sql.NullInt64
	var builderReceivedAt sql.NullTime
	if err := row.Scan(&operation.ID, &operation.ServiceID, &operation.ReleaseID,
		&kind, &state, &intent, &operation.Blocker, &operation.RequestedRef,
		&resolvedCommit, &checks, &operation.CreatedAt, &operation.UpdatedAt,
		&builderCommand, &builderSuccess, &builderAttempt, &commandJSON, &builderReceivedAt); err != nil {
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
	if builderCommand.Valid {
		var result protocol.CommandResult
		if err := json.Unmarshal(commandJSON, &result); err != nil {
			return v1.ReleaseOperation{}, fmt.Errorf("decode release builder result: %w", err)
		}
		if result.CommandID != builderCommand.String || !builderSuccess.Valid || !builderAttempt.Valid || !builderReceivedAt.Valid {
			return v1.ReleaseOperation{}, fmt.Errorf("release builder result has incomplete persisted identity")
		}
		operation.BuilderCommand = result.CommandID
		operation.BuilderAttempt = int(builderAttempt.Int64)
		if builderSuccess.Bool {
			if result.BuilderResult == nil {
				return v1.ReleaseOperation{}, fmt.Errorf("successful release builder result is missing builder_result")
			}
			operation.BuilderResult = result.BuilderResult
		} else {
			operation.BuilderError = "isolated builder reported a failed command"
		}
	}
	return operation, nil
}

func validBuilderReceiptID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (i > 0 && (character == '-' || character == '_')) {
			continue
		}
		return false
	}
	return true
}
