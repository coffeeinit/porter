package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVersionAndPaginationLimits(t *testing.T) {
	if Version != "v1" || BasePath != "/api/v1" {
		t.Fatalf("API version/path = %q %q", Version, BasePath)
	}
	if DefaultPageSize != 50 || MaxPageSize != 100 {
		t.Fatalf("pagination limits = default %d max %d", DefaultPageSize, MaxPageSize)
	}
	if DefaultPageSize > MaxPageSize {
		t.Fatal("default page size exceeds maximum")
	}
}

func TestResourceJSONUsesStableSnakeCaseFields(t *testing.T) {
	project := Project{
		ID:        "prj_0123456789abcdef",
		Name:      "demo",
		State:     ProjectActive,
		CreatedAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "state", "created_at", "updated_at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("Project JSON is missing %q: %s", key, encoded)
		}
	}
	if _, ok := got["createdAt"]; ok {
		t.Errorf("Project JSON must use snake_case: %s", encoded)
	}

	service := Service{ID: "svc_a", ProjectID: project.ID, Runtime: FirecrackerRuntime, DesiredInstances: 2}
	encoded, err = json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != string(FirecrackerRuntime) {
		t.Fatalf("Service runtime = %v, want %q", got["runtime"], FirecrackerRuntime)
	}
	if got["desired_instances"] != float64(2) {
		t.Fatalf("desired_instances = %v, want 2", got["desired_instances"])
	}
}

func TestReleaseAndInstanceJSONShapes(t *testing.T) {
	resolvedCommit := strings.Repeat("a", 40)
	release := Release{
		ID:             "rel_a",
		ServiceID:      "svc_a",
		Sequence:       7,
		State:          ReleaseBuilding,
		Source:         ReleaseSource{Kind: SourceGit, Repository: "https://example.invalid/team/app.git", Ref: "main"},
		ResolvedCommit: resolvedCommit,
		Build:          BuildSpec{Kind: BuildDockerfile, DockerfilePath: "Dockerfile"},
	}
	encoded, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "service_id", "sequence", "state", "source", "resolved_commit", "build", "created_at", "updated_at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("Release JSON is missing %q: %s", key, encoded)
		}
	}
	if got["resolved_commit"] != resolvedCommit {
		t.Errorf("resolved_commit = %v, want %q", got["resolved_commit"], resolvedCommit)
	}
	source, ok := got["source"].(map[string]any)
	if !ok || source["ref"] != "main" {
		t.Errorf("requested source ref was not kept separately: %v", got["source"])
	}

	instance := Instance{
		ID:        "ins_a",
		ServiceID: "svc_a",
		ReleaseID: release.ID,
		Ordinal:   1,
		Runtime:   FirecrackerRuntime,
		State:     InstanceProvisioning,
		Health:    HealthUnknown,
	}
	encoded, err = json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != string(FirecrackerRuntime) || got["ordinal"] != float64(1) {
		t.Fatalf("Instance JSON has unexpected runtime/ordinal: %s", encoded)
	}
}

func TestPageAndErrorEnvelopes(t *testing.T) {
	page := Page[Project]{Items: []Project{{ID: "prj_a", Name: "demo", State: ProjectActive}}, NextCursor: "opaque-token"}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["items"]; !ok || got["next_cursor"] != "opaque-token" {
		t.Fatalf("page JSON = %s", encoded)
	}

	response := ErrorResponse{Error: ErrorDetail{Code: ErrorConflict, Message: "state conflict", RequestID: "req_a"}}
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	body, ok := got["error"].(map[string]any)
	if !ok || body["code"] != string(ErrorConflict) || body["request_id"] != "req_a" {
		t.Fatalf("error JSON = %s", encoded)
	}
}

func TestEventJSONIncludesOptionalFleetNodeAndStorageReferences(t *testing.T) {
	event := Event{
		ID: "evt_a", Type: "backup.metadata_created", FleetNodeID: "node_a",
		DomainClaimID: "dcl_a", GatewayRouteID: "grt_a", VolumeID: "vol_a",
		SnapshotID: "snp_a", BackupID: "bkp_a",
		OccurredAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		Data:       json.RawMessage(`{"state":"metadata_only"}`),
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"fleet_node_id": "node_a", "domain_claim_id": "dcl_a", "gateway_route_id": "grt_a",
		"volume_id": "vol_a", "snapshot_id": "snp_a", "backup_id": "bkp_a",
	} {
		var actual string
		if err := json.Unmarshal(got[key], &actual); err != nil || actual != want {
			t.Fatalf("%s JSON = %s, err=%v, want %q", key, got[key], err, want)
		}
	}
	if string(got["data"]) != `{"state":"metadata_only"}` {
		t.Fatalf("event data JSON = %s", got["data"])
	}
	withoutRefs, err := json.Marshal(Event{ID: "evt_b", Type: "project.created"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"fleet_node_id", "domain_claim_id", "gateway_route_id", "volume_id", "snapshot_id", "backup_id"} {
		if strings.Contains(string(withoutRefs), field) {
			t.Fatalf("empty optional event reference %q should be omitted: %s", field, withoutRefs)
		}
	}
}

func TestStorageContractJSONKeepsUnavailableOperationsExplicit(t *testing.T) {
	nodeID := "node_a"
	volume := Volume{
		ID: "vol_a", ProjectID: "prj_a", ServiceID: "svc_a", Name: "data", SizeMiB: 2048,
		AccessMode: VolumeSingleWriter, State: VolumeMetadataOnly, NodeAffinityID: &nodeID,
		IntegrityStatus: IntegrityNotAvailable,
	}
	snapshot := SnapshotMetadata{
		ID: "snp_a", ProjectID: "prj_a", ServiceID: "svc_a", InstanceID: "ins_a",
		Consistency: SnapshotCrashConsistent, CaptureStatus: SnapshotCaptureUnavailable,
		NodeAffinityID: &nodeID, IntegrityStatus: IntegrityNotAvailable,
	}
	backup := BackupMetadata{
		ID: "bkp_a", ProjectID: "prj_a", ServiceID: "svc_a", InstanceID: "ins_a", SnapshotID: "snp_a",
		Consistency: SnapshotCrashConsistent, State: BackupMetadataOnly, OffHostStatus: BackupOffHostUnavailable,
		NodeAffinityID: &nodeID, IntegrityStatus: IntegrityNotAvailable,
	}
	for _, test := range []struct {
		name  string
		value any
		keys  []string
	}{
		{"volume", volume, []string{"access_mode", "state", "node_affinity_id", "integrity_status"}},
		{"snapshot", snapshot, []string{"consistency", "capture_status", "node_affinity_id", "integrity_status"}},
		{"backup", backup, []string{"consistency", "state", "off_host_status", "node_affinity_id", "integrity_status"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			for _, key := range test.keys {
				if _, ok := body[key]; !ok {
					t.Errorf("%s JSON is missing %q: %s", test.name, key, encoded)
				}
			}
			for _, unavailable := range []string{"artifact_sha256", "size_bytes", "integrity_checked_at"} {
				if _, ok := body[unavailable]; ok {
					t.Errorf("%s JSON must omit unavailable field %q: %s", test.name, unavailable, encoded)
				}
			}
		})
	}
}

func TestDomainClaimAndGatewayRouteJSONDistinguishProofFromLiveRouting(t *testing.T) {
	claim := DomainClaim{
		ID: "dcl_a", ProjectID: "prj_a", ServiceID: "svc_a", Hostname: "api.example.com",
		State: DomainClaimVerified, VerificationTXTName: "_porter-verify.api.example.com",
		VerificationTXTValue:   "porter-verify=" + strings.Repeat("a", 64),
		OwnershipProofTXTValue: "porter-verify=" + strings.Repeat("a", 64),
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	var claimJSON map[string]any
	if err := json.Unmarshal(encoded, &claimJSON); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"project_id", "service_id", "hostname", "state", "verification_txt_name", "verification_txt_value", "ownership_proof_txt_value"} {
		if _, ok := claimJSON[key]; !ok {
			t.Errorf("DomainClaim JSON is missing %q: %s", key, encoded)
		}
	}

	route := GatewayRoute{ID: "grt_a", DomainClaimID: claim.ID, ProjectID: claim.ProjectID, ServiceID: claim.ServiceID,
		Hostname: claim.Hostname, PathPrefix: "/", DesiredEnabled: true, GatewayStatus: GatewayUnavailable}
	encoded, err = json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	var routeJSON map[string]any
	if err := json.Unmarshal(encoded, &routeJSON); err != nil {
		t.Fatal(err)
	}
	if routeJSON["desired_enabled"] != true || routeJSON["gateway_status"] != GatewayUnavailable {
		t.Fatalf("route JSON must describe preview intent and unavailable gateway: %s", encoded)
	}
}

func TestRuntimeAndLifecycleContractValues(t *testing.T) {
	if FirecrackerRuntime != "firecracker" {
		t.Fatalf("production runtime = %q", FirecrackerRuntime)
	}
	if ReleaseActive != "active" || ReleaseSuperseded != "superseded" || ReleaseFailed != "failed" {
		t.Fatal("release lifecycle values changed unexpectedly")
	}
	if InstancePending != "pending" || InstanceProvisioning != "provisioning" || InstanceRunning != "running" || InstanceDeleted != "deleted" {
		t.Fatal("instance lifecycle values changed unexpectedly")
	}
}
