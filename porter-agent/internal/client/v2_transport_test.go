package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"porter/protocol"
)

func TestV2AgentTransportUsesExactRoutesHeadersAndPayloads(t *testing.T) {
	const nodeID = "node_01"
	const nodeToken = "node-bearer-token"
	leaseToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	command := protocol.CommandV2{
		ProtocolVersion: protocol.VersionV2,
		ID:              "cmd_01", NodeID: nodeID, OperationID: "op_01", Attempt: 1,
		Kind: "vm.start", LeaseToken: leaseToken, LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
		Payload: json.RawMessage(`{"vm_id":"vm_01"}`), State: "leased", CreatedAt: time.Now().UTC(),
	}
	var calls []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.String())
		if got := r.Header.Get("Authorization"); got != "Bearer "+nodeToken {
			t.Errorf("Authorization = %q, want node Bearer", got)
		}
		if got := r.Header.Get("X-Porter-Node-ID"); got != nodeID {
			t.Errorf("X-Porter-Node-ID = %q, want %q", got, nodeID)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/heartbeat":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode heartbeat: %v", err)
			} else if len(body) != 2 || string(body["protocol_version"]) != `"v2"` || len(body["at"]) == 0 {
				t.Errorf("heartbeat payload = %+v, want protocol_version and at only", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/telemetry":
			var batch protocol.TelemetryBatchV2
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Errorf("decode telemetry: %v", err)
			} else if batch.ProtocolVersion != protocol.VersionV2 || batch.NodeID != nodeID || len(batch.Logs) != 1 || len(batch.Metrics) != 1 {
				t.Errorf("telemetry batch = %+v", batch)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agent/commands":
			if r.URL.Query().Get("limit") != "20" {
				t.Errorf("commands limit = %q, want default 20", r.URL.Query().Get("limit"))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]protocol.CommandV2{command})
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/results":
			var result protocol.CommandResultV2
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Errorf("decode result: %v", err)
			} else if result.ProtocolVersion != protocol.VersionV2 || result.NodeID != nodeID || result.LeaseToken != leaseToken {
				t.Errorf("result receipt = %+v", result)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cl, err := NewWithTLS(server.URL, nodeID, nodeToken, TLSOptions{ServerCAFile: testServerCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cl.Heartbeat(ctx, protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	batch := protocol.TelemetryBatchV2{
		ProtocolVersion: protocol.VersionV2, NodeID: nodeID,
		Logs:    []protocol.TelemetryLogV2{{RecordID: "log_01", ReportedAt: time.Now().UTC(), Level: "info", Message: "hello"}},
		Metrics: []protocol.TelemetryMetricV2{{RecordID: "metric_01", ReportedAt: time.Now().UTC(), Name: "porter.agent.vm_count", Value: 1, Unit: "count"}},
	}
	if err := cl.Telemetry(ctx, batch); err != nil {
		t.Fatalf("telemetry: %v", err)
	}
	commands, err := cl.Commands(ctx)
	if err != nil || len(commands) != 1 || commands[0].ID != command.ID {
		t.Fatalf("commands = %+v, err=%v", commands, err)
	}
	if err := cl.Result(ctx, protocol.CommandResultV2{
		ProtocolVersion: protocol.VersionV2, CommandID: command.ID, OperationID: command.OperationID,
		NodeID: nodeID, Attempt: command.Attempt, Kind: command.Kind, LeaseToken: leaseToken,
		Success: true, State: "running",
	}); err != nil {
		t.Fatalf("result: %v", err)
	}
	want := []string{
		"POST /internal/agent/heartbeat",
		"POST /internal/agent/telemetry",
		"GET /internal/agent/commands?limit=20",
		"POST /internal/agent/results",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestAgentV2RoutesDoNotFollowRedirects(t *testing.T) {
	redirectTargetCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/capture" {
			redirectTargetCalls++
			return
		}
		w.Header().Set("Location", "/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	cl, err := NewWithTLS(server.URL, "node_01", "node-token", TLSOptions{ServerCAFile: testServerCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	err = cl.Heartbeat(context.Background(), protocol.AgentHeartbeatV2{ProtocolVersion: protocol.VersionV2, At: time.Now().UTC()})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.HTTPStatusCode() != http.StatusTemporaryRedirect {
		t.Fatalf("heartbeat error = %v, want redirect response without following", err)
	}
	if redirectTargetCalls != 0 {
		t.Fatalf("redirect target received %d requests, want none", redirectTargetCalls)
	}
}
