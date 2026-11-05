package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"porter/protocol"
)

func openCommandResultTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertCommandResultFixture(t *testing.T, s *Store, commandID, nodeID, operationID, commandState string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO operations(id,kind,resource_id,state,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, operationID, "vm.apply", "replica-1", "queued", "", now, now); err != nil {
		t.Fatalf("insert operation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO replicas(id,deployment_id,app_id,project_id,node_id,vm_id,state,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, "replica-1", "deployment-1", "app-1", "project-1", nodeID, "vm-1", "pending", "", now, now); err != nil {
		t.Fatalf("insert replica: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at)
		VALUES(?,?,?,?,?,?,?)`, commandID, nodeID, operationID, "vm.apply", `{}`, commandState, now); err != nil {
		t.Fatalf("insert command: %v", err)
	}
}

func commandResultStates(t *testing.T, s *Store, commandID, operationID string) (string, string, string, string, int) {
	t.Helper()
	var commandState, operationState, operationError, replicaState string
	var events int
	if err := s.db.QueryRow(`SELECT state FROM commands WHERE id=?`, commandID).Scan(&commandState); err != nil {
		t.Fatalf("read command state: %v", err)
	}
	if err := s.db.QueryRow(`SELECT state,error FROM operations WHERE id=?`, operationID).Scan(&operationState, &operationError); err != nil {
		t.Fatalf("read operation state: %v", err)
	}
	if err := s.db.QueryRow(`SELECT state FROM replicas WHERE id='replica-1'`).Scan(&replicaState); err != nil {
		t.Fatalf("read replica state: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE resource_id=?`, operationID).Scan(&events); err != nil {
		t.Fatalf("count operation events: %v", err)
	}
	return commandState, operationState, operationError, replicaState, events
}

func TestCompleteCommandRejectsInvalidIdentityAndState(t *testing.T) {
	cases := []struct {
		name         string
		commandState string
		commandNode  string
		result       protocol.CommandResult
		insert       bool
	}{
		{
			name:   "unknown command",
			result: protocol.CommandResult{CommandID: "missing", OperationID: "op-1", NodeID: "node-a", Success: true},
		},
		{
			name:         "command belongs to another node",
			commandState: "sent",
			commandNode:  "node-b",
			result:       protocol.CommandResult{CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-a", Success: true},
			insert:       true,
		},
		{
			name:         "operation id does not match command",
			commandState: "sent",
			commandNode:  "node-a",
			result:       protocol.CommandResult{CommandID: "cmd-1", OperationID: "other-op", NodeID: "node-a", Success: true},
			insert:       true,
		},
		{
			name:         "command was not sent",
			commandState: "queued",
			commandNode:  "node-a",
			result:       protocol.CommandResult{CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-a", Success: true},
			insert:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openCommandResultTestStore(t)
			if tc.insert {
				insertCommandResultFixture(t, s, "cmd-1", tc.commandNode, "op-1", tc.commandState)
			}
			if err := s.CompleteCommand(context.Background(), tc.result); err == nil {
				t.Fatal("CompleteCommand accepted an invalid result")
			}
			if tc.insert {
				commandState, operationState, operationError, replicaState, events := commandResultStates(t, s, "cmd-1", "op-1")
				if commandState != tc.commandState || operationState != "queued" || operationError != "" || replicaState != "pending" || events != 0 {
					t.Fatalf("rejected result mutated state: command=%q operation=%q error=%q replica=%q events=%d", commandState, operationState, operationError, replicaState, events)
				}
			}
		})
	}
}

func TestCompleteCommandIsIdempotentAndCannotRegressTerminalState(t *testing.T) {
	s := openCommandResultTestStore(t)
	insertCommandResultFixture(t, s, "cmd-1", "node-a", "op-1", "sent")
	result := protocol.CommandResult{CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-a", Success: true}

	if err := s.CompleteCommand(context.Background(), result); err != nil {
		t.Fatalf("complete valid result: %v", err)
	}
	if commandState, operationState, operationError, replicaState, events := commandResultStates(t, s, "cmd-1", "op-1"); commandState != "succeeded" || operationState != "succeeded" || operationError != "" || replicaState != "running" || events != 1 {
		t.Fatalf("unexpected completed state: command=%q operation=%q error=%q replica=%q events=%d", commandState, operationState, operationError, replicaState, events)
	}

	if err := s.CompleteCommand(context.Background(), result); err != nil {
		t.Fatalf("exact duplicate result should be harmless: %v", err)
	}
	if commandState, operationState, operationError, replicaState, events := commandResultStates(t, s, "cmd-1", "op-1"); commandState != "succeeded" || operationState != "succeeded" || operationError != "" || replicaState != "running" || events != 1 {
		t.Fatalf("duplicate result changed terminal state: command=%q operation=%q error=%q replica=%q events=%d", commandState, operationState, operationError, replicaState, events)
	}

	conflict := protocol.CommandResult{CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-a", Success: false, Error: "late conflicting result"}
	if err := s.CompleteCommand(context.Background(), conflict); err == nil {
		t.Fatal("conflicting terminal result was accepted")
	}
	if commandState, operationState, operationError, replicaState, events := commandResultStates(t, s, "cmd-1", "op-1"); commandState != "succeeded" || operationState != "succeeded" || operationError != "" || replicaState != "running" || events != 1 {
		t.Fatalf("conflicting result regressed terminal state: command=%q operation=%q error=%q replica=%q events=%d", commandState, operationState, operationError, replicaState, events)
	}
}

func TestClaimCommandsIncrementsAttemptOnlyOnce(t *testing.T) {
	s := openCommandResultTestStore(t)
	insertCommandResultFixture(t, s, "cmd-1", "node-a", "op-1", "queued")

	claimed, err := s.ClaimCommands(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("claim commands: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "cmd-1" || claimed[0].State != "sent" || claimed[0].Attempt != 1 {
		t.Fatalf("unexpected first claim: %+v", claimed)
	}
	claimed, err = s.ClaimCommands(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("repeat claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("sent command was claimed twice: %+v", claimed)
	}
	var state string
	var attempt int
	if err := s.db.QueryRow(`SELECT state,attempt FROM commands WHERE id='cmd-1'`).Scan(&state, &attempt); err != nil {
		t.Fatalf("read claimed command: %v", err)
	}
	if state != "sent" || attempt != 1 {
		t.Fatalf("stored claim = state %q, attempt %d; want sent, 1", state, attempt)
	}
}

func TestCompleteCommandRejectsResultFromEarlierRetryAttempt(t *testing.T) {
	s := openCommandResultTestStore(t)
	insertCommandResultFixture(t, s, "cmd-1", "node-a", "op-1", "sent")
	if _, err := s.db.Exec(`UPDATE commands SET attempt=1,created_at=? WHERE id='cmd-1'`, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("prepare first attempt: %v", err)
	}
	if count := s.RequeueStaleCommands(context.Background(), time.Now().Add(-time.Hour)); count != 1 {
		t.Fatalf("requeue count = %d, want 1", count)
	}
	claimed, err := s.ClaimCommands(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Attempt != 2 {
		t.Fatalf("retry did not advance attempt generation: %+v", claimed)
	}
	if count := s.RequeueStaleCommands(context.Background(), time.Now().Add(-time.Minute)); count != 0 {
		t.Fatalf("fresh retry was immediately requeued %d time(s)", count)
	}

	stale := protocol.CommandResult{CommandID: "cmd-1", OperationID: "op-1", NodeID: "node-a", Attempt: 1, Success: true}
	if err := s.CompleteCommand(context.Background(), stale); !errors.Is(err, ErrCommandAttemptMismatch) {
		t.Fatalf("old attempt result error = %v, want ErrCommandAttemptMismatch", err)
	}
	active := stale
	active.Attempt = claimed[0].Attempt
	if err := s.CompleteCommand(context.Background(), active); err != nil {
		t.Fatalf("active attempt result was rejected: %v", err)
	}
}

func TestOpenMigratesCommandAttemptColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE commands(
		id TEXT PRIMARY KEY, node_id TEXT NOT NULL, operation_id TEXT NOT NULL,
		kind TEXT NOT NULL, payload TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'queued',
		created_at TIMESTAMP NOT NULL)`)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create legacy commands table: %v", err)
	}
	if _, err := legacyDB.Exec(`INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at)
		VALUES('cmd-legacy','node-a','op-legacy','vm.apply','{}','sent',?)`, time.Now().Add(-time.Hour)); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("insert legacy command: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open and migrate legacy database: %v", err)
	}
	defer s.Close()
	var attempt int
	var claimedAt int64
	if err := s.db.QueryRow(`SELECT attempt,claimed_at FROM commands WHERE id='cmd-legacy'`).Scan(&attempt, &claimedAt); err != nil {
		t.Fatalf("read migrated command columns: %v", err)
	}
	if attempt != 0 || claimedAt != 0 {
		t.Fatalf("legacy command defaults = attempt %d, claimed_at %d; want 0 and 0", attempt, claimedAt)
	}
}

func TestClaimCommandsConcurrentPollsDoNotDuplicate(t *testing.T) {
	s := openCommandResultTestStore(t)
	insertCommandResultFixture(t, s, "cmd-1", "node-a", "op-1", "queued")
	type claimResult struct {
		commands []protocol.Command
		err      error
	}
	const pollers = 12
	start := make(chan struct{})
	results := make(chan claimResult, pollers)
	for i := 0; i < pollers; i++ {
		go func() {
			<-start
			commands, err := s.ClaimCommands(context.Background(), "node-a")
			results <- claimResult{commands: commands, err: err}
		}()
	}
	close(start)
	total := 0
	for i := 0; i < pollers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent claim failed: %v", result.err)
		}
		total += len(result.commands)
	}
	if total != 1 {
		t.Fatalf("concurrent polls claimed %d command instances, want exactly 1", total)
	}
}

func TestHealthyReplicasForAppExcludesChecking(t *testing.T) {
	s := openCommandResultTestStore(t)
	now := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO deployments(id,app_id,image,status,created_at) VALUES(?,?,?,?,?)`, "deployment-1", "app-1", "image", "active", now); err != nil {
		t.Fatal(err)
	}
	for _, replica := range []struct {
		id     string
		health string
	}{{id: "replica-checking", health: "checking"}, {id: "replica-healthy", health: "healthy"}} {
		_, err := s.db.Exec(`INSERT INTO replicas(id,deployment_id,app_id,project_id,node_id,vm_id,state,error,created_at,updated_at,guest_ip,health_status,traffic_weight)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, replica.id, "deployment-1", "app-1", "project-1", "node-a", replica.id, "running", "", now, now, "10.0.0.2", replica.health, 100)
		if err != nil {
			t.Fatalf("insert %s replica: %v", replica.health, err)
		}
	}

	got := s.HealthyReplicasForApp(context.Background(), "app-1")
	if len(got) != 1 || got[0].ReplicaID != "replica-healthy" {
		t.Fatalf("gateway-eligible replicas = %+v, want only the healthy replica", got)
	}
}
