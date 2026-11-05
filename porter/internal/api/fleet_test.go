package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

func TestFleetMaintenanceStateSurvivesAuthenticatedHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "cordoned", path: "cordon", wantStatus: http.StatusOK},
		{name: "draining", path: "drain", wantStatus: http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer st.Close()

			const adminToken = "test-admin-token"
			httpServer := httptest.NewServer(New(st, adminToken, "porter.local").Handler())
			defer httpServer.Close()

			joinBody := doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/fleet/join-tokens", adminToken, "", map[string]int{"ttl_seconds": 3600}, http.StatusCreated)
			var join struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(joinBody, &join); err != nil {
				t.Fatalf("decode join token response: %v", err)
			}
			if join.Token == "" {
				t.Fatal("join token response was empty")
			}

			enrollBody := doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/fleet/enroll", "", "", protocol.EnrollRequest{
					Token: join.Token, Hostname: "test-node", OS: "linux", Arch: "amd64",
					CPUs: 4, MemoryMB: 4096, DiskMB: 8192,
				}, http.StatusCreated)
			var enrollment protocol.EnrollResponse
			if err := json.Unmarshal(enrollBody, &enrollment); err != nil {
				t.Fatalf("decode enrollment response: %v", err)
			}
			if enrollment.NodeID == "" || enrollment.NodeToken == "" {
				t.Fatal("enrollment response is missing node credentials")
			}

			doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/fleet/nodes/"+enrollment.NodeID+"/"+tc.path,
				adminToken, "", map[string]string{}, tc.wantStatus)

			doJSONRequest(t, httpServer.Client(), http.MethodPost,
				httpServer.URL+"/v1/fleet/heartbeat", enrollment.NodeToken, enrollment.NodeID,
				protocol.Heartbeat{NodeID: enrollment.NodeID, CPUsFree: 3, MemoryFreeMB: 3072, DiskFreeMB: 7000, VMCount: 1}, http.StatusOK)

			nodesBody := doJSONRequest(t, httpServer.Client(), http.MethodGet,
				httpServer.URL+"/v1/nodes", adminToken, "", nil, http.StatusOK)
			var nodes []store.Node
			if err := json.Unmarshal(nodesBody, &nodes); err != nil {
				t.Fatalf("decode nodes response: %v", err)
			}
			if len(nodes) != 1 {
				t.Fatalf("got %d nodes, want 1", len(nodes))
			}
			if nodes[0].State != tc.name {
				t.Errorf("heartbeat changed maintenance state: got %q, want %q", nodes[0].State, tc.name)
			}
			if nodes[0].CPUsFree != 3 || nodes[0].MemoryFreeMB != 3072 || nodes[0].DiskFreeMB != 7000 || nodes[0].VMCount != 1 {
				t.Errorf("heartbeat capacity not visible through API: %+v", nodes[0])
			}
		})
	}
}

func doJSONRequest(t *testing.T, client *http.Client, method, url, token, nodeID string, payload any, wantStatus int) []byte {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if nodeID != "" {
		request.Header.Set("X-Porter-Node-ID", nodeID)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: got status %d, want %d; body: %s", method, url, response.StatusCode, wantStatus, responseBody)
	}
	return responseBody
}
