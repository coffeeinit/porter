package store

import (
	"context"
	"database/sql"
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

// TestPostgresOperationCommandLeaseIntegration uses only an explicitly
// disposable porter_test_store_* database. It never connects to a live agent.
func TestPostgresOperationCommandLeaseIntegration(t *testing.T) {
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
		t.Fatalf("apply PostgreSQL schema through migration 0009: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, "operations project")
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "operations service"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, release.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinReleaseCommit(ctx, release.ID, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	if err := repo.SetReleaseArtifactDigest(ctx, release.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkReleaseGuestReady(ctx, release.ID, digest); err != nil {
		t.Fatal(err)
	}
	for _, state := range []v1.ReleaseState{v1.ReleaseReady, v1.ReleaseDeploying} {
		if err := repo.TransitionRelease(ctx, release.ID, state); err != nil {
			t.Fatalf("transition release to %s: %v", state, err)
		}
	}
	if err := repo.RecordReleaseHealthGate(ctx, release.ID, digest, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, release.ID, v1.ReleaseActive); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.CreateInstance(ctx, service.ID, release.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	node, err := repo.CreateFleetNode(ctx, "operations-node")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetFleetNodeState(ctx, node.ID, v1.FleetNodeReady); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceNode(ctx, instance.ID, node.ID); err != nil {
		t.Fatal(err)
	}

	spec := AgentCommandSpec{Kind: v1.AgentCommandVMApply, VMApply: &protocol.VMApplySpec{
		VMID: "vm_1", ReplicaID: instance.ID, ProjectID: project.ID, DeploymentID: release.ID,
		Image: "sha256:image", KernelPath: "/var/lib/porter/vmlinux", RootfsPath: "/var/lib/porter/rootfs.ext4",
		VCPUs: 2, MemoryMB: 1024, DiskMB: 8192,
	}}
	queued, err := repo.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "apply-once", Command: spec,
	})
	if err != nil {
		t.Fatalf("queue typed operation: %v", err)
	}
	if queued.State != v1.OperationQueued || queued.CommandState != v1.AgentCommandQueued || queued.Attempt != 0 || queued.CommandID == "" {
		t.Fatalf("queued operation state = %+v", queued)
	}
	duplicate, err := repo.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "apply-once", Command: spec,
	})
	if err != nil || duplicate.ID != queued.ID || duplicate.CommandID != queued.CommandID {
		t.Fatalf("idempotent queue retry = %+v, %v; want original IDs", duplicate, err)
	}
	changed := spec
	changed.VMApply = new(protocol.VMApplySpec)
	*changed.VMApply = *spec.VMApply
	changed.VMApply.Image = "sha256:other-image"
	if _, err := repo.QueueOperation(ctx, QueueOperationRequest{InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "apply-once", Command: changed}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("reused idempotency key error=%v, want conflict", err)
	}
	if _, err := repo.QueueOperation(ctx, QueueOperationRequest{InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "shell", Command: AgentCommandSpec{Kind: v1.AgentCommandKind("shell.exec")}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("arbitrary shell command error=%v, want ErrInvalidArgument", err)
	}

	// A rejected operation event must roll back both operation and command rows.
	secondInstance, err := repo.CreateInstance(ctx, service.ID, release.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceNode(ctx, secondInstance.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE FUNCTION porter_control_plane.reject_operation_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.event_type = 'operation.queued' THEN RAISE EXCEPTION 'reject operation event'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_operation_event BEFORE INSERT ON porter_control_plane.resource_events FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_operation_event()`); err != nil {
		t.Fatal(err)
	}
	secondSpec := spec
	secondSpec.VMApply = new(protocol.VMApplySpec)
	*secondSpec.VMApply = *spec.VMApply
	secondSpec.VMApply.ReplicaID = secondInstance.ID
	if _, err := repo.QueueOperation(ctx, QueueOperationRequest{InstanceID: secondInstance.ID, NodeID: node.ID, IdempotencyKey: "rollback-event", Command: secondSpec}); err == nil {
		t.Fatal("operation queue succeeded despite rejecting its event")
	}
	var rolledBack int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.operations WHERE instance_id = $1`, secondInstance.ID).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("operation insert not rolled back with event: count=%d err=%v", rolledBack, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_operation_event ON porter_control_plane.resource_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP FUNCTION porter_control_plane.reject_operation_event()`); err != nil {
		t.Fatal(err)
	}

	// Lease #1 is intentionally already expired. Results before retry must be rejected.
	firstLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Second, time.Now().UTC().Add(-time.Minute))
	if err != nil || len(firstLease) != 1 || firstLease[0].ID != queued.CommandID || firstLease[0].Attempt != 1 || !validLeaseToken(firstLease[0].LeaseToken) {
		t.Fatalf("first command lease = %+v, %v", firstLease, err)
	}
	if strings.Contains(string(firstLease[0].Payload), "shell") {
		t.Fatalf("typed VM payload unexpectedly contains a shell command: %s", firstLease[0].Payload)
	}
	firstResult := v1.AgentCommandResult{
		CommandID: queued.CommandID, OperationID: queued.ID, NodeID: node.ID, Kind: v1.AgentCommandVMApply, Attempt: 1,
		LeaseToken: firstLease[0].LeaseToken, Success: true, State: v1.CommandResultRunning,
	}
	if err := repo.RecordCommandResult(ctx, firstResult); !errors.Is(err, ErrCommandLeaseExpired) {
		t.Fatalf("expired lease result error=%v, want ErrCommandLeaseExpired", err)
	}
	count, err := repo.RequeueExpiredCommands(ctx, firstLease[0].LeaseExpiresAt.Add(time.Second), 10)
	if err != nil || count != 1 {
		t.Fatalf("requeue expired lease count=%d err=%v", count, err)
	}
	operation, err := repo.GetOperation(ctx, queued.ID)
	if err != nil || operation.State != v1.OperationQueued || operation.Attempt != 1 || operation.CommandState != v1.AgentCommandQueued {
		t.Fatalf("requeued operation=%+v err=%v; attempt must increment only on next lease", operation, err)
	}

	secondLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Minute, time.Now().UTC())
	if err != nil || len(secondLease) != 1 || secondLease[0].Attempt != 2 || secondLease[0].LeaseToken == firstLease[0].LeaseToken {
		t.Fatalf("retry lease=%+v err=%v; want attempt 2 and a new token", secondLease, err)
	}
	staleProtocolResult := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: secondLease[0].ID, OperationID: secondLease[0].OperationID,
		NodeID: secondLease[0].NodeID, Attempt: 1, Kind: secondLease[0].Kind,
		LeaseToken: firstLease[0].LeaseToken, Success: true, State: "running",
	}
	if err := repo.RecordProtocolCommandResult(ctx, staleProtocolResult); !errors.Is(err, ErrCommandAttemptMismatch) {
		t.Fatalf("stale v2 result error=%v, want ErrCommandAttemptMismatch", err)
	}
	wrongKindProtocolResult := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: secondLease[0].ID, OperationID: secondLease[0].OperationID,
		NodeID: secondLease[0].NodeID, Attempt: secondLease[0].Attempt, Kind: "vm.stop",
		LeaseToken: secondLease[0].LeaseToken, Success: true, State: "stopped",
	}
	if err := repo.RecordProtocolCommandResult(ctx, wrongKindProtocolResult); !errors.Is(err, ErrCommandKindMismatch) {
		t.Fatalf("wrong-kind v2 result error=%v, want ErrCommandKindMismatch", err)
	}
	staleAttempt := firstResult
	staleAttempt.Attempt = 1
	if err := repo.RecordCommandResult(ctx, staleAttempt); !errors.Is(err, ErrCommandAttemptMismatch) {
		t.Fatalf("stale attempt result error=%v, want ErrCommandAttemptMismatch", err)
	}
	wrongToken := firstResult
	wrongToken.Attempt = 2
	if err := repo.RecordCommandResult(ctx, wrongToken); !errors.Is(err, ErrCommandLeaseMismatch) {
		t.Fatalf("stale lease result error=%v, want ErrCommandLeaseMismatch", err)
	}
	wrongNode := firstResult
	wrongNode.Attempt = 2
	wrongNode.LeaseToken = secondLease[0].LeaseToken
	wrongNode.NodeID = "node_wrong"
	if err := repo.RecordCommandResult(ctx, wrongNode); !errors.Is(err, ErrCommandNodeMismatch) {
		t.Fatalf("wrong-node result error=%v, want ErrCommandNodeMismatch", err)
	}
	wrongKind := firstResult
	wrongKind.Attempt = 2
	wrongKind.LeaseToken = secondLease[0].LeaseToken
	wrongKind.Kind = v1.AgentCommandVMStop
	if err := repo.RecordCommandResult(ctx, wrongKind); !errors.Is(err, ErrCommandKindMismatch) {
		t.Fatalf("wrong-kind result error=%v, want ErrCommandKindMismatch", err)
	}

	// Failure to append the terminal event must roll back result, command,
	// operation, and Instance-state changes together.
	if _, err := db.ExecContext(ctx, `
CREATE FUNCTION porter_control_plane.reject_success_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.event_type = 'operation.succeeded' THEN RAISE EXCEPTION 'reject success event'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_success_event BEFORE INSERT ON porter_control_plane.resource_events FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_success_event()`); err != nil {
		t.Fatal(err)
	}
	secondResult := v1.AgentCommandResult{
		CommandID: queued.CommandID, OperationID: queued.ID, NodeID: node.ID, Kind: v1.AgentCommandVMApply, Attempt: 2,
		LeaseToken: secondLease[0].LeaseToken, Success: true, State: v1.CommandResultRunning,
	}
	if err := repo.RecordCommandResult(ctx, secondResult); err == nil {
		t.Fatal("command result succeeded despite rejecting its terminal event")
	}
	operation, err = repo.GetOperation(ctx, queued.ID)
	if err != nil || operation.State != v1.OperationRunning || operation.CommandState != v1.AgentCommandLeased {
		t.Fatalf("result transaction did not roll back operation/command: %+v err=%v", operation, err)
	}
	var resultCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.agent_command_results WHERE command_id = $1`, queued.CommandID).Scan(&resultCount); err != nil || resultCount != 0 {
		t.Fatalf("result receipt not rolled back: count=%d err=%v", resultCount, err)
	}
	gotInstance, err := repo.GetInstance(ctx, instance.ID)
	if err != nil || gotInstance.State != v1.InstancePending {
		t.Fatalf("Instance changed despite transaction rollback: instance=%+v err=%v", gotInstance, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_success_event ON porter_control_plane.resource_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP FUNCTION porter_control_plane.reject_success_event()`); err != nil {
		t.Fatal(err)
	}

	protocolResult := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: secondLease[0].ID, OperationID: secondLease[0].OperationID,
		NodeID: secondLease[0].NodeID, Attempt: secondLease[0].Attempt, Kind: secondLease[0].Kind,
		LeaseToken: secondLease[0].LeaseToken, Success: true, State: "running",
	}
	if err := repo.RecordProtocolCommandResult(ctx, protocolResult); err != nil {
		t.Fatalf("record valid command result: %v", err)
	}
	if err := repo.RecordProtocolCommandResult(ctx, protocolResult); err != nil {
		t.Fatalf("exact duplicate command result must be idempotent: %v", err)
	}
	conflictingDuplicate := protocolResult
	conflictingDuplicate.Success = false
	conflictingDuplicate.State = "failed"
	conflictingDuplicate.Error = "late conflicting result"
	if err := repo.RecordProtocolCommandResult(ctx, conflictingDuplicate); !errors.Is(err, ErrCommandResultConflict) {
		t.Fatalf("conflicting duplicate result error=%v, want ErrCommandResultConflict", err)
	}
	operation, err = repo.GetOperation(ctx, queued.ID)
	if err != nil || operation.State != v1.OperationSucceeded || operation.CommandState != v1.AgentCommandSucceeded || operation.Attempt != 2 {
		t.Fatalf("completed operation=%+v err=%v", operation, err)
	}
	gotInstance, err = repo.GetInstance(ctx, instance.ID)
	if err != nil || gotInstance.State != v1.InstanceRunning || gotInstance.Health != v1.HealthUnknown {
		t.Fatalf("successful result did not persist fenced Instance state: %+v err=%v", gotInstance, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.agent_command_results WHERE command_id = $1`, queued.CommandID).Scan(&resultCount); err != nil || resultCount != 1 {
		t.Fatalf("exact duplicate inserted extra result receipt: count=%d err=%v", resultCount, err)
	}
	var linkedEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE operation_id = $1 AND command_id = $2`, queued.ID, queued.CommandID).Scan(&linkedEvents); err != nil || linkedEvents != 5 {
		t.Fatalf("linked durable events=%d err=%v; want queue, two lease, retry, result", linkedEvents, err)
	}
	events, err := repo.ListEventsPage(ctx, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundResultEvent := false
	for _, event := range events {
		if event.OperationID == queued.ID && event.CommandID == queued.CommandID && event.Type == "operation.succeeded" {
			foundResultEvent = true
		}
	}
	if !foundResultEvent {
		t.Fatal("operation result event did not expose its operation and command references")
	}
	operations, err := repo.ListOperationsPage(ctx, 10, nil)
	if err != nil || len(operations) != 1 || operations[0].ID != queued.ID {
		t.Fatalf("operation list=%+v err=%v", operations, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.agent_command_results SET error = 'mutated' WHERE command_id = $1`, queued.CommandID); err == nil {
		t.Fatal("append-only command result accepted UPDATE")
	}
}

func TestPostgresSnapshotLifecycleIntegration(t *testing.T) {
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
		t.Fatalf("apply PostgreSQL schema through migration 0009: %v", err)
	}
	repo, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	project, err := repo.CreateProject(ctx, "snapshot integration project")
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "snapshot integration service"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, release.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinReleaseCommit(ctx, release.ID, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	if err := repo.SetReleaseArtifactDigest(ctx, release.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkReleaseGuestReady(ctx, release.ID, digest); err != nil {
		t.Fatal(err)
	}
	for _, state := range []v1.ReleaseState{v1.ReleaseReady, v1.ReleaseDeploying} {
		if err := repo.TransitionRelease(ctx, release.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RecordReleaseHealthGate(ctx, release.ID, digest, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, release.ID, v1.ReleaseActive); err != nil {
		t.Fatal(err)
	}
	instance, err := repo.CreateInstance(ctx, service.ID, release.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	node, err := repo.CreateFleetNode(ctx, "snapshot-integration-node")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetFleetNodeState(ctx, node.ID, v1.FleetNodeReady); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceNode(ctx, instance.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	vmSpec := &protocol.VMApplySpec{
		VMID: "vm_snapshot_integration", ReplicaID: instance.ID, ProjectID: project.ID, DeploymentID: release.ID,
		Image: "sha256:image", KernelPath: "/local/kernel", RootfsPath: "/local/rootfs.ext4", VCPUs: 1, MemoryMB: 512, DiskMB: 8192,
	}
	apply, err := repo.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "snapshot-test-apply",
		Command: AgentCommandSpec{Kind: v1.AgentCommandVMApply, VMApply: vmSpec},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Minute, time.Now().UTC())
	if err != nil || len(applyLease) != 1 || applyLease[0].ID != apply.CommandID {
		t.Fatalf("apply lease=%+v err=%v", applyLease, err)
	}
	if err := repo.RecordProtocolCommandResult(ctx, protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: applyLease[0].ID, OperationID: applyLease[0].OperationID,
		NodeID: applyLease[0].NodeID, Attempt: applyLease[0].Attempt, Kind: applyLease[0].Kind,
		LeaseToken: applyLease[0].LeaseToken, Success: true, State: "running",
	}); err != nil {
		t.Fatalf("record apply result: %v", err)
	}

	snapshot, captureOperation, err := repo.QueueSnapshotCreateOperation(ctx, instance.ID, protocol.SnapshotCrashConsistent, "snapshot-capture-once")
	if err != nil || snapshot.CaptureStatus != "queued" || captureOperation.State != v1.OperationQueued {
		t.Fatalf("queue capture metadata=%+v operation=%+v err=%v", snapshot, captureOperation, err)
	}
	duplicateSnapshot, duplicateCapture, err := repo.QueueSnapshotCreateOperation(ctx, instance.ID, protocol.SnapshotCrashConsistent, "snapshot-capture-once")
	if err != nil || duplicateSnapshot.ID != snapshot.ID || duplicateCapture.ID != captureOperation.ID {
		t.Fatalf("idempotent capture metadata=%+v operation=%+v err=%v", duplicateSnapshot, duplicateCapture, err)
	}
	captureLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Minute, time.Now().UTC())
	if err != nil || len(captureLease) != 1 || captureLease[0].Kind != "snapshot.create" || captureLease[0].OperationID != captureOperation.ID {
		t.Fatalf("capture lease=%+v err=%v", captureLease, err)
	}
	leasedSnapshot, err := repo.GetSnapshotMetadata(ctx, snapshot.ID)
	if err != nil || leasedSnapshot.CaptureStatus != "capturing" {
		t.Fatalf("capture lease did not transition metadata: %+v err=%v", leasedSnapshot, err)
	}
	record := protocol.SnapshotRecord{
		ID: snapshot.ID, VMID: vmSpec.VMID, SizeBytes: 8192,
		StateSHA256: strings.Repeat("a", 64), MemSHA256: strings.Repeat("b", 64),
		DiskFiles: []protocol.SnapshotFile{{Kind: "rootfs", SizeBytes: 4096, SHA256: strings.Repeat("c", 64)}},
	}
	captureReceipt, err := protocol.SnapshotArtifactReceiptFor(record)
	if err != nil {
		t.Fatal(err)
	}
	captureResult := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: captureLease[0].ID, OperationID: captureLease[0].OperationID,
		NodeID: captureLease[0].NodeID, Attempt: captureLease[0].Attempt, Kind: captureLease[0].Kind,
		LeaseToken: captureLease[0].LeaseToken, Success: true, State: "snapshot_ready", SnapshotArtifact: &captureReceipt,
	}
	if err := repo.RecordProtocolCommandResult(ctx, captureResult); err != nil {
		t.Fatalf("record capture receipt: %v", err)
	}
	if err := repo.RecordProtocolCommandResult(ctx, captureResult); err != nil {
		t.Fatalf("exact duplicate capture receipt: %v", err)
	}
	captured, err := repo.GetSnapshotMetadata(ctx, snapshot.ID)
	if err != nil || captured.CaptureStatus != "captured" || captured.IntegrityStatus != "verified" ||
		captured.ArtifactSHA256 == nil || *captured.ArtifactSHA256 != captureReceipt.ArtifactSHA256 ||
		captured.SizeBytes == nil || *captured.SizeBytes != captureReceipt.SizeBytes {
		t.Fatalf("capture receipt did not converge into metadata: %+v err=%v", captured, err)
	}
	var artifactJSON string
	if err := db.QueryRowContext(ctx, `SELECT snapshot_artifact::text FROM porter_control_plane.agent_command_results WHERE command_id = $1`, captureLease[0].ID).Scan(&artifactJSON); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"state_path", "mem_path", "socket", "/local/"} {
		if strings.Contains(strings.ToLower(artifactJSON), forbidden) {
			t.Fatalf("PostgreSQL receipt leaked local path/socket material %q: %s", forbidden, artifactJSON)
		}
	}

	if _, _, err := repo.QueueSnapshotRestoreOperation(ctx, instance.ID, snapshot.ID, "restore-before-stop"); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("restore on running Instance error=%v, want failed precondition", err)
	}
	stopOperation, err := repo.QueueOperation(ctx, QueueOperationRequest{
		InstanceID: instance.ID, NodeID: node.ID, IdempotencyKey: "snapshot-test-stop",
		Command: AgentCommandSpec{Kind: v1.AgentCommandVMStop, VMApply: vmSpec},
	})
	if err != nil {
		t.Fatalf("queue VM stop: %v", err)
	}
	stopLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Minute, time.Now().UTC())
	if err != nil || len(stopLease) != 1 || stopLease[0].OperationID != stopOperation.ID || stopLease[0].Kind != "vm.stop" {
		t.Fatalf("stop lease=%+v err=%v", stopLease, err)
	}
	if err := repo.RecordProtocolCommandResult(ctx, protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: stopLease[0].ID, OperationID: stopLease[0].OperationID,
		NodeID: stopLease[0].NodeID, Attempt: stopLease[0].Attempt, Kind: stopLease[0].Kind,
		LeaseToken: stopLease[0].LeaseToken, Success: true, State: "stopped",
	}); err != nil {
		t.Fatalf("record VM stop result: %v", err)
	}

	restoredSnapshot, restoreOperation, err := repo.QueueSnapshotRestoreOperation(ctx, instance.ID, snapshot.ID, "snapshot-restore-once")
	if err != nil || restoredSnapshot.RestoreStatus != "queued" || restoredSnapshot.RestoreOperationID == nil ||
		*restoredSnapshot.RestoreOperationID != restoreOperation.ID || restoreOperation.State != v1.OperationQueued {
		t.Fatalf("queue restore metadata=%+v operation=%+v err=%v", restoredSnapshot, restoreOperation, err)
	}
	duplicateRestoredSnapshot, duplicateRestore, err := repo.QueueSnapshotRestoreOperation(ctx, instance.ID, snapshot.ID, "snapshot-restore-once")
	if err != nil || duplicateRestoredSnapshot.ID != snapshot.ID || duplicateRestore.ID != restoreOperation.ID {
		t.Fatalf("idempotent restore metadata=%+v operation=%+v err=%v", duplicateRestoredSnapshot, duplicateRestore, err)
	}
	restoreLease, err := repo.LeaseProtocolCommandsAt(ctx, node.ID, 10, time.Minute, time.Now().UTC())
	if err != nil || len(restoreLease) != 1 || restoreLease[0].Kind != "snapshot.restore" || restoreLease[0].OperationID != restoreOperation.ID {
		t.Fatalf("restore lease=%+v err=%v", restoreLease, err)
	}
	runningRestore, err := repo.GetSnapshotMetadata(ctx, snapshot.ID)
	if err != nil || runningRestore.RestoreStatus != "running" {
		t.Fatalf("restore lease did not transition metadata: %+v err=%v", runningRestore, err)
	}
	restoreResult := protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: restoreLease[0].ID, OperationID: restoreLease[0].OperationID,
		NodeID: restoreLease[0].NodeID, Attempt: restoreLease[0].Attempt, Kind: restoreLease[0].Kind,
		LeaseToken: restoreLease[0].LeaseToken, Success: true, State: "restored", SnapshotArtifact: &captureReceipt,
	}
	if err := repo.RecordProtocolCommandResult(ctx, restoreResult); err != nil {
		t.Fatalf("record restore receipt: %v", err)
	}
	finalSnapshot, err := repo.GetSnapshotMetadata(ctx, snapshot.ID)
	if err != nil || finalSnapshot.CaptureStatus != "captured" || finalSnapshot.RestoreStatus != "restored" || finalSnapshot.IntegrityStatus != "verified" {
		t.Fatalf("restore did not converge snapshot metadata: %+v err=%v", finalSnapshot, err)
	}
	finalInstance, err := repo.GetInstance(ctx, instance.ID)
	if err != nil || finalInstance.State != v1.InstanceRunning {
		t.Fatalf("restore did not return Instance to running: %+v err=%v", finalInstance, err)
	}
}
