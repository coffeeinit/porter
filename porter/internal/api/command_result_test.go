package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

func TestFleetCommandResultAuthorizationAndIntegrity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	const adminToken = "test-admin-token"
	httpServer := httptest.NewServer(New(st, adminToken, "").Handler())
	defer httpServer.Close()

	ctx := context.Background()
	enrollNode := func(name string) protocol.EnrollResponse {
		t.Helper()
		joinToken, err := st.CreateJoinToken(ctx, time.Hour)
		if err != nil {
			t.Fatalf("create join token for %s: %v", name, err)
		}
		enrollment, err := st.Enroll(ctx, protocol.EnrollRequest{
			Token: joinToken, Hostname: name, OS: "linux", Arch: "amd64",
			CPUs: 4, MemoryMB: 4096, DiskMB: 8192,
		})
		if err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
		return enrollment
	}
	nodes := []protocol.EnrollResponse{enrollNode("node-a"), enrollNode("node-b")}

	project, err := st.CreateProject(ctx, "command-integrity-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(ctx, project.ID, "web", "example.invalid:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := st.CreateDeployment(ctx, app.ID, "example.invalid:latest", 1); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	var assignedNode protocol.EnrollResponse
	var otherNode protocol.EnrollResponse
	var command protocol.Command
	for i, node := range nodes {
		body := doJSONRequest(t, httpServer.Client(), http.MethodGet,
			httpServer.URL+"/v1/fleet/commands", node.NodeToken, node.NodeID, nil, http.StatusOK)
		var commands []protocol.Command
		if err := json.Unmarshal(body, &commands); err != nil {
			t.Fatalf("decode commands: %v", err)
		}
		if len(commands) > 0 {
			assignedNode = node
			otherNode = nodes[1-i]
			command = commands[0]
			break
		}
	}
	if command.ID == "" {
		t.Fatal("deployment did not queue a command for either enrolled node")
	}

	unknown := protocol.CommandResult{
		CommandID: "missing-command", OperationID: command.OperationID,
		NodeID: assignedNode.NodeID, Success: true,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, unknown, http.StatusNotFound)

	crossNode := protocol.CommandResult{
		CommandID: command.ID, OperationID: command.OperationID,
		NodeID: otherNode.NodeID, Success: true,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", otherNode.NodeToken, otherNode.NodeID, crossNode, http.StatusNotFound)

	wrongOperation := protocol.CommandResult{
		CommandID: command.ID, OperationID: "wrong-operation", NodeID: assignedNode.NodeID, Success: true,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, wrongOperation, http.StatusConflict)

	staleAttempt := protocol.CommandResult{
		CommandID: command.ID, OperationID: command.OperationID,
		NodeID: assignedNode.NodeID, Attempt: command.Attempt - 1, Success: true,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, staleAttempt, http.StatusConflict)

	valid := protocol.CommandResult{
		CommandID: command.ID, OperationID: command.OperationID,
		NodeID: assignedNode.NodeID, Attempt: command.Attempt, Success: true,
	}
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, valid, http.StatusOK)
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, valid, http.StatusOK)

	conflicting := valid
	conflicting.Success = false
	conflicting.Error = "late conflicting result"
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", assignedNode.NodeToken, assignedNode.NodeID, conflicting, http.StatusConflict)
}
