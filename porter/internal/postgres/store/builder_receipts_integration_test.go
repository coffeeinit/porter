package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"
	"porter/protocol"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestReleaseBuilderReceiptIsPinnedAppendOnlyAndNotProductionEvidence(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_STORE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_STORE_TEST_POSTGRES_DSN to a disposable porter_test_store_* database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to PostgreSQL test database: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_store_") {
		t.Fatalf("refusing destructive integration test in database %q; name must start with porter_test_store_", databaseName)
	}
	if err := resetStoreIntegrationObjects(ctx, db); err != nil {
		t.Fatalf("reset disposable integration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetStoreIntegrationObjects(cleanupCtx, db); err != nil {
			t.Errorf("clean up disposable integration database: %v", err)
		}
	})
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply embedded PostgreSQL schema: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(ctx, "builder receipt project")
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "builder-receipt-service"})
	if err != nil {
		t.Fatal(err)
	}
	repositoryURL := "https://github.com/example/app.git"
	commit := strings.Repeat("a", 40)
	release, err := repo.CreateRelease(ctx, service.ID, v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: repositoryURL, Ref: "main"},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "Dockerfile"},
		Intent: v1.ReleaseIntentPreview,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, release.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinReleaseCommit(ctx, release.ID, commit); err != nil {
		t.Fatal(err)
	}
	node, err := repo.CreateFleetNode(ctx, "builder receipt test node")
	if err != nil {
		t.Fatal(err)
	}

	spec := protocol.BuilderBuildSpec{
		BuildID: "build_receipt_test_01", RepositoryURL: repositoryURL, Commit: commit,
		ContextSubdir: "services/web", DockerfilePath: "Dockerfile",
		ImageName: "ghcr.io/example/app:build_receipt_test_01", Architecture: "amd64",
		VCPUs: 2, MemoryMB: 2048, DiskMB: 8192, TimeoutSeconds: 900,
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("invalid test builder spec: %v", err)
	}
	const operationID = "rop_builder_receipt_01"
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO porter_control_plane.release_operations
    (id, service_id, release_id, kind, state, intent, blocker, requested_ref,
     resolved_commit, checks, idempotency_key, request_sha256, builder_spec)
VALUES ($1, $2, $3, 'build', 'running', 'preview', 'builder_running', 'main',
        $4, '[]'::jsonb, 'builder-receipt-operation', $5, $6::jsonb)`,
		operationID, service.ID, release.ID, commit, strings.Repeat("f", 64), string(specJSON)); err != nil {
		t.Fatalf("insert pinned running operation fixture: %v", err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	commandResult := protocol.CommandResult{
		CommandID: "cmd_builder_receipt_01", OperationID: operationID, NodeID: node.ID,
		Attempt: 1, Success: true, State: "built",
		BuilderResult: &protocol.BuilderBuildResult{
			BuildID: spec.BuildID, OCIReference: "ghcr.io/example/app@" + digest,
			Digest: digest, Architecture: spec.Architecture, SizeBytes: 8192,
		},
	}
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, commandResult); err != nil {
		t.Fatalf("persist valid pinned builder receipt: %v", err)
	}
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, commandResult); err != nil {
		t.Fatalf("exact result retry should be idempotent: %v", err)
	}
	var receipts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.release_builder_results WHERE operation_id = $1`, operationID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("builder receipt count=%d err=%v; want exactly one", receipts, err)
	}

	conflict := commandResult
	conflict.BuilderResult = &protocol.BuilderBuildResult{
		BuildID: spec.BuildID, OCIReference: "ghcr.io/example/app@sha256:" + strings.Repeat("c", 64),
		Digest: "sha256:" + strings.Repeat("c", 64), Architecture: spec.Architecture, SizeBytes: 8192,
	}
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, conflict); !errors.Is(err, ErrCommandResultConflict) {
		t.Fatalf("conflicting duplicate error=%v; want ErrCommandResultConflict", err)
	}
	missingResult := commandResult
	missingResult.BuilderResult = nil
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, missingResult); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("success without typed builder_result error=%v; want ErrInvalidArgument", err)
	}
	failureWithResult := commandResult
	failureWithResult.Success = false
	failureWithResult.State = "failed"
	failureWithResult.Error = "builder failed"
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, failureWithResult); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("failed result with builder_result error=%v; want ErrInvalidArgument", err)
	}
	mismatchedSpec := spec
	mismatchedSpec.Commit = strings.Repeat("c", 40)
	if err := repo.RecordReleaseBuilderCommandResult(ctx, mismatchedSpec, commandResult); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("result with changed source pin error=%v; want ErrFailedPrecondition", err)
	}

	operations, err := repo.ListReleaseOperations(ctx, release.ID, 100)
	if err != nil {
		t.Fatalf("list release operations with builder receipt: %v", err)
	}
	var projected *v1.ReleaseOperation
	for i := range operations {
		if operations[i].ID == operationID {
			projected = &operations[i]
			break
		}
	}
	if projected == nil || projected.State != v1.ReleaseOperationSucceeded || projected.Blocker != "" ||
		projected.BuilderAttempt != 1 || projected.BuilderCommand != commandResult.CommandID || projected.BuilderResult == nil || *projected.BuilderResult != *commandResult.BuilderResult {
		t.Fatalf("operation history did not project the typed builder receipt: %+v", projected)
	}
	release, err = repo.GetRelease(ctx, release.ID)
	if err != nil || release.State != v1.ReleaseBuilding || release.ArtifactDigest != "" || release.GuestReady || release.HealthGate == v1.ReleaseHealthGatePassed {
		t.Fatalf("OCI builder receipt was incorrectly promoted to Firecracker release evidence: release=%+v err=%v", release, err)
	}
	promotion, err := repo.RequestProductionPromotion(ctx, release.ID, "builder-receipt-promotion-check")
	if err != nil {
		t.Fatalf("record production promotion checks: %v", err)
	}
	if promotion.State != v1.ReleaseOperationBlocked || promotion.Blocker != "artifact_digest_pinned" {
		t.Fatalf("OCI builder receipt bypassed Firecracker promotion gates: %+v", promotion)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.release_builder_results SET success = success WHERE command_id = $1`, commandResult.CommandID); err == nil {
		t.Fatal("append-only builder receipt accepted an update")
	}

	failedRelease, err := repo.CreateRelease(ctx, service.ID, v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: repositoryURL, Ref: "main"},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "Dockerfile"},
		Intent: v1.ReleaseIntentPreview,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, failedRelease.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinReleaseCommit(ctx, failedRelease.ID, commit); err != nil {
		t.Fatal(err)
	}
	failedSpec := spec
	failedSpec.BuildID = "build_receipt_failure_01"
	failedOperationID := "rop_builder_receipt_failed"
	failedSpecJSON, err := json.Marshal(failedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO porter_control_plane.release_operations
    (id, service_id, release_id, kind, state, intent, blocker, requested_ref,
     resolved_commit, checks, idempotency_key, request_sha256, builder_spec)
VALUES ($1, $2, $3, 'build', 'running', 'preview', 'builder_running', 'main',
        $4, '[]'::jsonb, 'builder-receipt-failed-operation', $5, $6::jsonb)`,
		failedOperationID, service.ID, failedRelease.ID, commit, strings.Repeat("e", 64), string(failedSpecJSON)); err != nil {
		t.Fatalf("insert failed builder operation fixture: %v", err)
	}
	failedResult := protocol.CommandResult{
		CommandID: "cmd_builder_receipt_failed", OperationID: failedOperationID, NodeID: node.ID,
		Attempt: 1, Success: false, Error: "builder failed after timeout",
	}
	if err := repo.RecordReleaseBuilderCommandResult(ctx, failedSpec, failedResult); err != nil {
		t.Fatalf("persist valid failed builder result: %v", err)
	}
	failedOperations, err := repo.ListReleaseOperations(ctx, failedRelease.ID, 100)
	if err != nil {
		t.Fatalf("list failed builder operation: %v", err)
	}
	if len(failedOperations) != 1 || failedOperations[0].State != v1.ReleaseOperationFailed || failedOperations[0].Blocker != "builder_failed" || failedOperations[0].BuilderResult != nil || failedOperations[0].BuilderAttempt != 1 {
		t.Fatalf("failed builder result was not projected honestly: %+v", failedOperations)
	}
	failedRelease, err = repo.GetRelease(ctx, failedRelease.ID)
	if err != nil || failedRelease.State != v1.ReleaseBuilding || failedRelease.ArtifactDigest != "" || failedRelease.GuestReady {
		t.Fatalf("failed builder result changed Firecracker release evidence: release=%+v err=%v", failedRelease, err)
	}
	if err := repo.RecordReleaseBuilderCommandResult(ctx, spec, protocol.CommandResult{
		CommandID: "cmd_builder_receipt_02", OperationID: promotion.ID, NodeID: node.ID,
		Attempt: 1, Success: true, State: "built", BuilderResult: commandResult.BuilderResult,
	}); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("builder result for a non-build operation error=%v; want ErrFailedPrecondition", err)
	}
}
