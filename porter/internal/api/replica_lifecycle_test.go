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

func TestReplicaLifecycleAPIQueuesAndCompletesStopStartRestart(t *testing.T) {
	st, httpServer, enrollment, app, replica := newReplicaLifecycleAPIFixture(t)
	completeReplicaApplyThroughAPI(t, httpServer, enrollment, replica)

	for _, tc := range []struct {
		action string
		state  string
	}{
		{action: "stop", state: "stopped"},
		{action: "start", state: "running"},
		{action: "restart", state: "running"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			body := doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/replicas/"+replica.ID+"/"+tc.action,
				"test-admin-token", "", nil, http.StatusAccepted)
			var accepted struct {
				Status      string `json:"status"`
				OperationID string `json:"operation_id"`
			}
			if err := json.Unmarshal(body, &accepted); err != nil {
				t.Fatalf("decode accepted response: %v", err)
			}
			if accepted.Status != "accepted" || accepted.OperationID == "" {
				t.Fatalf("unexpected accepted response: %+v", accepted)
			}

			commandBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
				httpServer.URL+"/v1/fleet/commands", enrollment.NodeToken, enrollment.NodeID, nil, http.StatusOK)
			var commands []protocol.Command
			if err := json.Unmarshal(commandBody, &commands); err != nil {
				t.Fatalf("decode lifecycle command: %v", err)
			}
			if len(commands) != 1 {
				t.Fatalf("got %d queued commands, want 1: %+v", len(commands), commands)
			}
			command := commands[0]
			if command.Kind != "vm."+tc.action || command.OperationID != accepted.OperationID || command.NodeID != enrollment.NodeID {
				t.Fatalf("unexpected dispatched command: %+v", command)
			}
			var spec protocol.VMApplySpec
			payload, err := json.Marshal(command.Payload)
			if err != nil {
				t.Fatalf("encode command payload: %v", err)
			}
			if err := json.Unmarshal(payload, &spec); err != nil {
				t.Fatalf("decode command payload: %v", err)
			}
			if spec.VMID != replica.VMID || spec.ReplicaID != replica.ID || spec.Image != "example.invalid:latest" {
				t.Fatalf("lifecycle command lost the persisted VM apply spec: %+v", spec)
			}

			doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/fleet/result", enrollment.NodeToken, enrollment.NodeID,
				protocol.CommandResult{
					CommandID: command.ID, OperationID: command.OperationID, NodeID: enrollment.NodeID,
					Attempt: command.Attempt, Success: true, State: tc.state,
				}, http.StatusOK)

			replicaBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
				httpServer.URL+"/v1/apps/"+app.ID+"/replicas", "test-admin-token", "", nil, http.StatusOK)
			var replicas []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			}
			if err := json.Unmarshal(replicaBody, &replicas); err != nil {
				t.Fatalf("decode replicas: %v", err)
			}
			if len(replicas) != 1 || replicas[0].ID != replica.ID || replicas[0].State != tc.state {
				t.Fatalf("replica state after %s: got %+v, want %q", tc.action, replicas, tc.state)
			}

			operationBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
				httpServer.URL+"/v1/operations", "test-admin-token", "", nil, http.StatusOK)
			var operations []store.Operation
			if err := json.Unmarshal(operationBody, &operations); err != nil {
				t.Fatalf("decode operations: %v", err)
			}
			found := false
			for _, operation := range operations {
				if operation.ID == accepted.OperationID {
					found = true
					if operation.Kind != "vm."+tc.action || operation.ResourceID != replica.ID || operation.State != "succeeded" {
						t.Errorf("unexpected completed operation: %+v", operation)
					}
				}
			}
			if !found {
				t.Fatalf("operation %s not listed by API", accepted.OperationID)
			}
		})
	}

	if st == nil {
		t.Fatal("fixture store was nil")
	}
}

func TestReplicaLifecycleAPIRejectsUnauthorizedInvalidAndConcurrentActions(t *testing.T) {
	_, httpServer, enrollment, _, replica := newReplicaLifecycleAPIFixture(t)
	completeReplicaApplyThroughAPI(t, httpServer, enrollment, replica)

	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/"+replica.ID+"/stop", "", "", nil, http.StatusUnauthorized)
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/"+replica.ID+"/suspend", "test-admin-token", "", nil, http.StatusBadRequest)
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/rep_missing/stop", "test-admin-token", "", nil, http.StatusNotFound)

	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/"+replica.ID+"/stop", "test-admin-token", "", nil, http.StatusAccepted)
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/"+replica.ID+"/restart", "test-admin-token", "", nil, http.StatusConflict)
}

func newReplicaLifecycleAPIFixture(t *testing.T) (*store.Store, *httptest.Server, protocol.EnrollResponse, store.App, store.Replica) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	httpServer := httptest.NewServer(New(st, "test-admin-token", "").Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = st.Close()
	})

	ctx := context.Background()
	joinToken, err := st.CreateJoinToken(ctx, time.Hour)
	if err != nil {
		t.Fatalf("create join token: %v", err)
	}
	enrollment, err := st.Enroll(ctx, protocol.EnrollRequest{
		Token: joinToken, Hostname: "lifecycle-test-node", OS: "linux", Arch: "amd64",
		CPUs: 4, MemoryMB: 4096, DiskMB: 8192,
	})
	if err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	project, err := st.CreateProject(ctx, "replica-lifecycle-test")
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
	replicas, err := st.ListAppReplicas(ctx, app.ID)
	if err != nil {
		t.Fatalf("list app replicas: %v", err)
	}
	if len(replicas) != 1 || replicas[0].NodeID != enrollment.NodeID || replicas[0].VMID == "" {
		t.Fatalf("deployment did not schedule one replica on the test node: %+v", replicas)
	}
	return st, httpServer, enrollment, app, replicas[0]
}

func completeReplicaApplyThroughAPI(t *testing.T, httpServer *httptest.Server, enrollment protocol.EnrollResponse, replica store.Replica) {
	t.Helper()
	body := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/fleet/commands", enrollment.NodeToken, enrollment.NodeID, nil, http.StatusOK)
	var commands []protocol.Command
	if err := json.Unmarshal(body, &commands); err != nil {
		t.Fatalf("decode apply command: %v", err)
	}
	if len(commands) != 1 || commands[0].Kind != "vm.apply" {
		t.Fatalf("expected one initial vm.apply command, got %+v", commands)
	}
	command := commands[0]
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", enrollment.NodeToken, enrollment.NodeID,
		protocol.CommandResult{
			CommandID: command.ID, OperationID: command.OperationID, NodeID: enrollment.NodeID,
			Attempt: command.Attempt, Success: true, State: "running",
		}, http.StatusOK)
}

func TestReplicaLifecycleAPIFailurePreservesReplicaState(t *testing.T) {
	_, httpServer, enrollment, app, replica := newReplicaLifecycleAPIFixture(t)
	completeReplicaApplyThroughAPI(t, httpServer, enrollment, replica)

	body := doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/replicas/"+replica.ID+"/stop", "test-admin-token", "", nil, http.StatusAccepted)
	var accepted struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	commandBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/fleet/commands", enrollment.NodeToken, enrollment.NodeID, nil, http.StatusOK)
	var commands []protocol.Command
	if err := json.Unmarshal(commandBody, &commands); err != nil {
		t.Fatalf("decode stop command: %v", err)
	}
	if len(commands) != 1 || commands[0].Kind != "vm.stop" {
		t.Fatalf("expected one vm.stop command, got %+v", commands)
	}
	command := commands[0]
	doJSONRequest(t, httpServer.Client(), http.MethodPost,
		httpServer.URL+"/v1/fleet/result", enrollment.NodeToken, enrollment.NodeID,
		protocol.CommandResult{
			CommandID: command.ID, OperationID: command.OperationID, NodeID: enrollment.NodeID,
			Attempt: command.Attempt, Success: false, Error: "simulated stop failure",
		}, http.StatusOK)

	replicaBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/apps/"+app.ID+"/replicas", "test-admin-token", "", nil, http.StatusOK)
	var replicas []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(replicaBody, &replicas); err != nil {
		t.Fatalf("decode replicas: %v", err)
	}
	if len(replicas) != 1 || replicas[0].ID != replica.ID || replicas[0].State != "running" {
		t.Fatalf("failed stop changed replica state: %+v", replicas)
	}

	operationBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/v1/operations", "test-admin-token", "", nil, http.StatusOK)
	var operations []store.Operation
	if err := json.Unmarshal(operationBody, &operations); err != nil {
		t.Fatalf("decode operations: %v", err)
	}
	for _, operation := range operations {
		if operation.ID == accepted.OperationID {
			if operation.State != "failed" || operation.Error != "simulated stop failure" {
				t.Fatalf("failed lifecycle operation was not recorded: %+v", operation)
			}
			return
		}
	}
	t.Fatalf("failed lifecycle operation %s not listed", accepted.OperationID)
}
