package agentapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"
	pgstore "porter/controlplane/internal/postgres/store"
	"porter/protocol"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresAgentHTTPSLeaseResultIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_AGENTAPI_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_AGENTAPI_TEST_POSTGRES_DSN to a disposable porter_test_agentapi_* database")
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
		t.Fatal(err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(databaseName, "porter_test_agentapi_") {
		t.Fatalf("refusing destructive integration test in database %q", databaseName)
	}
	if err := resetAgentAPIIntegrationDB(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetAgentAPIIntegrationDB(cleanupCtx, db); err != nil {
			t.Errorf("reset disposable agent API test database: %v", err)
		}
	})
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply schema through migration 0009: %v", err)
	}
	repo, err := pgstore.New(db)
	if err != nil {
		t.Fatal(err)
	}

	project, err := repo.CreateProject(ctx, "agent API project")
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "agent API service"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := repo.CreateRelease(ctx, service.ID, v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: "https://example.test/source.git", Ref: "main"},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "Dockerfile"},
	})
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
	node, err := repo.CreateFleetNode(ctx, "agent API node")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	handler, err := NewWithConfig(repo, "operator-secret", Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()
	client := tlsServer.Client()

	credentialResp := postAgentJSON(t, client, tlsServer.URL+"/internal/admin/fleet/nodes/"+node.ID+"/credentials", nil, "operator-secret", "")
	defer credentialResp.Body.Close()
	if credentialResp.StatusCode != http.StatusCreated || credentialResp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("credential provision status=%d cache=%q", credentialResp.StatusCode, credentialResp.Header.Get("Cache-Control"))
	}
	var issued pgstore.NodeCredentialIssue
	if err := json.NewDecoder(credentialResp.Body).Decode(&issued); err != nil || issued.NodeID != node.ID || issued.Token == "" {
		t.Fatalf("issued credential=%+v err=%v", issued, err)
	}

	heartbeat, _ := json.Marshal(protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: now})
	heartbeatResp := postAgentJSON(t, client, tlsServer.URL+"/internal/agent/heartbeat", heartbeat, issued.Token, node.ID)
	heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat status=%d", heartbeatResp.StatusCode)
	}
	var nodeState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.fleet_nodes WHERE id = $1`, node.ID).Scan(&nodeState); err != nil || nodeState != string(v1.FleetNodeReady) {
		t.Fatalf("heartbeat node state=%q err=%v", nodeState, err)
	}

	firstInstance, err := repo.CreateInstance(ctx, service.ID, release.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceNode(ctx, firstInstance.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	firstOperation := queueAgentApply(t, ctx, repo, project.ID, release.ID, firstInstance.ID, node.ID, "op-first")
	firstLease := pollAgentCommand(t, client, tlsServer.URL, issued.Token, node.ID)
	if firstLease.Attempt != 1 || firstLease.OperationID != firstOperation.ID || firstLease.Kind != "vm.apply" {
		t.Fatalf("first lease=%+v", firstLease)
	}
	firstResult := successResult(firstLease, "running")
	for range 2 {
		resp := postResult(t, client, tlsServer.URL, issued.Token, node.ID, firstResult)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("exact duplicate result status=%d", resp.StatusCode)
		}
	}
	secondInstance, err := repo.CreateInstance(ctx, service.ID, release.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceNode(ctx, secondInstance.ID, node.ID); err != nil {
		t.Fatal(err)
	}
	secondOperation := queueAgentApply(t, ctx, repo, project.ID, release.ID, secondInstance.ID, node.ID, "op-retry")
	secondLease1 := pollAgentCommand(t, client, tlsServer.URL, issued.Token, node.ID)
	if secondLease1.Attempt != 1 || secondLease1.OperationID != secondOperation.ID {
		t.Fatalf("second operation first lease=%+v", secondLease1)
	}
	now = secondLease1.LeaseExpiresAt.Add(time.Second)
	secondLease2 := pollAgentCommand(t, client, tlsServer.URL, issued.Token, node.ID)
	if secondLease2.Attempt != 2 || secondLease2.ID != secondLease1.ID || secondLease2.LeaseToken == secondLease1.LeaseToken {
		t.Fatalf("retried lease=%+v; first=%+v", secondLease2, secondLease1)
	}
	staleRetry := successResult(secondLease1, "running")
	resp := postResult(t, client, tlsServer.URL, issued.Token, node.ID, staleRetry)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || resp.Header.Get("X-Porter-Result-Stale") != "true" {
		t.Fatalf("old retry result status=%d marker=%q", resp.StatusCode, resp.Header.Get("X-Porter-Result-Stale"))
	}

	if _, err := db.ExecContext(ctx, `
CREATE FUNCTION porter_control_plane.reject_agentapi_success() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.event_type = 'operation.succeeded' THEN RAISE EXCEPTION 'reject event'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_agentapi_success BEFORE INSERT ON porter_control_plane.resource_events FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_agentapi_success()`); err != nil {
		t.Fatal(err)
	}
	secondResult := successResult(secondLease2, "running")
	resp = postResult(t, client, tlsServer.URL, issued.Token, node.ID, secondResult)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("result with rejected event status=%d", resp.StatusCode)
	}
	var resultCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.agent_command_results WHERE command_id = $1`, secondLease2.ID).Scan(&resultCount); err != nil || resultCount != 0 {
		t.Fatalf("result receipt escaped failed transaction: count=%d err=%v", resultCount, err)
	}
	var operationState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.operations WHERE id = $1`, secondOperation.ID).Scan(&operationState); err != nil || operationState != string(v1.OperationRunning) {
		t.Fatalf("operation escaped failed event transaction: state=%q err=%v", operationState, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_agentapi_success ON porter_control_plane.resource_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP FUNCTION porter_control_plane.reject_agentapi_success()`); err != nil {
		t.Fatal(err)
	}
	resp = postResult(t, client, tlsServer.URL, issued.Token, node.ID, secondResult)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("result retry after event transaction rollback status=%d", resp.StatusCode)
	}
	var linkedEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE operation_id = $1 AND command_id = $2`, secondOperation.ID, secondLease2.ID).Scan(&linkedEvents); err != nil || linkedEvents < 4 {
		t.Fatalf("transactional operation/command events=%d err=%v", linkedEvents, err)
	}
}

func queueAgentApply(t *testing.T, ctx context.Context, repo *pgstore.Store, projectID, releaseID, instanceID, nodeID, idem string) v1.Operation {
	t.Helper()
	operation, err := repo.QueueOperation(ctx, pgstore.QueueOperationRequest{
		InstanceID: instanceID, NodeID: nodeID, IdempotencyKey: idem,
		Command: pgstore.AgentCommandSpec{Kind: v1.AgentCommandVMApply, VMApply: &protocol.VMApplySpec{
			VMID: "vm-" + instanceID, ReplicaID: instanceID, ProjectID: projectID, DeploymentID: releaseID,
			Image: "sha256:image", KernelPath: "/var/lib/porter/vmlinux", RootfsPath: "/var/lib/porter/rootfs.ext4", VCPUs: 1, MemoryMB: 512, DiskMB: 8192,
		}},
	})
	if err != nil {
		t.Fatalf("queue VM apply operation: %v", err)
	}
	return operation
}

func pollAgentCommand(t *testing.T, client *http.Client, serverURL, token, nodeID string) protocol.CommandV2 {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+"/internal/agent/commands?limit=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Porter-Node-ID", nodeID)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("poll command status=%d body=%s", resp.StatusCode, body)
	}
	var commands []protocol.CommandV2
	if err := json.NewDecoder(resp.Body).Decode(&commands); err != nil || len(commands) != 1 {
		t.Fatalf("poll commands=%+v err=%v", commands, err)
	}
	return commands[0]
}

func successResult(command protocol.CommandV2, state string) protocol.CommandResultV2 {
	return protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: command.ID, OperationID: command.OperationID, NodeID: command.NodeID,
		Attempt: command.Attempt, Kind: command.Kind, LeaseToken: command.LeaseToken, Success: true, State: state,
	}
}

func postResult(t *testing.T, client *http.Client, serverURL, token, nodeID string, result protocol.CommandResultV2) *http.Response {
	t.Helper()
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return postAgentJSON(t, client, serverURL+"/internal/agent/results", body, token, nodeID)
}

func postAgentJSON(t *testing.T, client *http.Client, url string, body []byte, token, nodeID string) *http.Response {
	t.Helper()
	method := http.MethodPost
	req, err := http.NewRequest(method, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token == "operator-secret" {
		req.Header.Set("Authorization", "Bearer operator-secret")
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Porter-Node-ID", nodeID)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func resetAgentAPIIntegrationDB(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`DROP SCHEMA IF EXISTS porter_control_plane CASCADE`,
		`DROP TABLE IF EXISTS public.porter_control_plane_schema_migrations`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}
