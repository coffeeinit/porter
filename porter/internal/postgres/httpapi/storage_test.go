package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	v1 "porter/controlplane/contract/v1"
)

func TestStorageMetadataRoutesValidateScopeAndStayFailClosed(t *testing.T) {
	nodeID := "node_a"
	repo := &stubRepository{
		project:  v1.Project{ID: "prj_a", State: v1.ProjectActive},
		service:  v1.Service{ID: "svc_a", ProjectID: "prj_a", State: v1.ServiceActive},
		instance: v1.Instance{ID: "ins_a", ServiceID: "svc_a", NodeID: nodeID},
		snapshot: v1.SnapshotMetadata{
			ID: "snp_a", ProjectID: "prj_a", ServiceID: "svc_a", InstanceID: "ins_a",
			Consistency: v1.SnapshotCrashConsistent, CaptureStatus: v1.SnapshotCaptureUnavailable,
			NodeAffinityID: &nodeID, IntegrityStatus: v1.IntegrityNotAvailable,
		},
	}
	server := mustServer(t, repo)

	invalidVolumeBodies := []string{
		`{"name":"data","size_mib":1024,"access_mode":"shared_writable"}`,
		`{"name":"data","size_mib":1024,"mount_path":"/../../etc"}`,
		`{"name":"data","size_mib":0,"access_mode":"single_writer"}`,
	}
	for _, body := range invalidVolumeBodies {
		response := serve(server, http.MethodPost, "/api/v1/projects/prj_a/services/svc_a/volumes", "Bearer "+testAdminToken, "application/json", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid volume body %s status=%d body=%s", body, response.Code, response.Body.String())
		}
		assertErrorEnvelope(t, response, v1.ErrorBadRequest)
	}
	if repo.volumeCalls != 0 {
		t.Fatalf("invalid volume requests reached repository %d times", repo.volumeCalls)
	}

	crossProject := serve(server, http.MethodPost, "/api/v1/projects/prj_other/services/svc_a/volumes", "Bearer "+testAdminToken,
		"application/json", `{"name":"data","size_mib":1024,"access_mode":"single_writer"}`)
	if crossProject.Code != http.StatusNotFound {
		t.Fatalf("cross-project volume status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}
	assertErrorEnvelope(t, crossProject, v1.ErrorNotFound)
	if repo.volumeCalls != 0 {
		t.Fatalf("cross-project volume reached repository %d times", repo.volumeCalls)
	}

	createdVolume := serve(server, http.MethodPost, "/api/v1/projects/prj_a/services/svc_a/volumes", "Bearer "+testAdminToken,
		"application/json", `{"name":"data","size_mib":1024,"mount_path":"/data","access_mode":"single_writer","node_affinity_id":"node_a"}`)
	if createdVolume.Code != http.StatusCreated {
		t.Fatalf("create volume status=%d body=%s", createdVolume.Code, createdVolume.Body.String())
	}
	var volume v1.Volume
	if err := json.Unmarshal(createdVolume.Body.Bytes(), &volume); err != nil {
		t.Fatal(err)
	}
	if volume.State != v1.VolumeMetadataOnly || volume.AccessMode != v1.VolumeSingleWriter || volume.IntegrityStatus != v1.IntegrityNotAvailable || volume.NodeAffinityID == nil || *volume.NodeAffinityID != nodeID {
		t.Fatalf("volume response claims unsupported state or lost affinity: %+v", volume)
	}
	if strings.Contains(createdVolume.Body.String(), "attached") || strings.Contains(createdVolume.Body.String(), "provisioned") {
		t.Fatalf("volume response contains fake side-effect claim: %s", createdVolume.Body.String())
	}

	for _, body := range []string{`{"consistency":"application_consistent"}`, `{}`, `{"consistency":"crash_consistent","unexpected":true}`} {
		response := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots", "Bearer "+testAdminToken, "application/json", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsupported snapshot body %s status=%d body=%s", body, response.Code, response.Body.String())
		}
		assertErrorEnvelope(t, response, v1.ErrorBadRequest)
	}
	if repo.snapshotCalls != 0 {
		t.Fatalf("invalid snapshot request reached repository %d times", repo.snapshotCalls)
	}
	createdSnapshot := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots", "Bearer "+testAdminToken,
		"application/json", `{"consistency":"crash_consistent"}`)
	if createdSnapshot.Code != http.StatusCreated {
		t.Fatalf("create snapshot metadata status=%d body=%s", createdSnapshot.Code, createdSnapshot.Body.String())
	}
	var snapshot v1.SnapshotMetadata
	if err := json.Unmarshal(createdSnapshot.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.CaptureStatus != v1.SnapshotCaptureUnavailable || snapshot.IntegrityStatus != v1.IntegrityNotAvailable || snapshot.ArtifactSHA256 != nil || snapshot.NodeAffinityID == nil || *snapshot.NodeAffinityID != nodeID {
		t.Fatalf("snapshot record claims capture/integrity or lost affinity: %+v", snapshot)
	}

	invalidBackup := serve(server, http.MethodPost, "/api/v1/snapshots/snp_a/backup-records", "Bearer "+testAdminToken,
		"application/json", `{"consistency":"application_consistent"}`)
	if invalidBackup.Code != http.StatusBadRequest {
		t.Fatalf("application-consistent backup status=%d body=%s", invalidBackup.Code, invalidBackup.Body.String())
	}
	assertErrorEnvelope(t, invalidBackup, v1.ErrorBadRequest)
	if repo.backupCalls != 0 {
		t.Fatalf("invalid backup request reached repository %d times", repo.backupCalls)
	}
	createdBackup := serve(server, http.MethodPost, "/api/v1/snapshots/snp_a/backup-records", "Bearer "+testAdminToken,
		"application/json", `{"consistency":"crash_consistent"}`)
	if createdBackup.Code != http.StatusCreated {
		t.Fatalf("create backup metadata status=%d body=%s", createdBackup.Code, createdBackup.Body.String())
	}
	var backup v1.BackupMetadata
	if err := json.Unmarshal(createdBackup.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if backup.State != v1.BackupMetadataOnly || backup.OffHostStatus != v1.BackupOffHostUnavailable || backup.IntegrityStatus != v1.IntegrityNotAvailable || backup.ArtifactSHA256 != nil || backup.NodeAffinityID == nil || *backup.NodeAffinityID != nodeID {
		t.Fatalf("backup record claims unavailable behavior or lost affinity: %+v", backup)
	}

	for _, path := range []string{"/api/v1/volumes/vol_a/attach", "/api/v1/backups/bkp_a/restore"} {
		response := serve(server, http.MethodPost, path, "Bearer "+testAdminToken, "application/json", `{}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unsupported operation %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestSnapshotOperationRoutesQueueAuthenticatedCrashConsistentWork(t *testing.T) {
	nodeID := "node_a"
	digest := strings.Repeat("a", 64)
	repo := &stubRepository{
		service:  v1.Service{ID: "svc_a", ProjectID: "prj_a", State: v1.ServiceActive},
		instance: v1.Instance{ID: "ins_a", ServiceID: "svc_a", NodeID: nodeID},
		snapshot: v1.SnapshotMetadata{
			ID: "snp_a", ProjectID: "prj_a", ServiceID: "svc_a", InstanceID: "ins_a",
			Consistency: v1.SnapshotCrashConsistent, CaptureStatus: "captured", RestoreStatus: "not_requested",
			NodeAffinityID: &nodeID, ArtifactSHA256: &digest, SizeBytes: int64Ptr(2048), IntegrityStatus: "verified",
		},
	}
	server := mustServer(t, repo)

	unauthorized := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots/capture", "", "application/json",
		`{"consistency":"crash_consistent","idempotency_key":"capture-1"}`)
	if unauthorized.Code != http.StatusUnauthorized || repo.captureCalls != 0 {
		t.Fatalf("unauthenticated capture status=%d calls=%d body=%s", unauthorized.Code, repo.captureCalls, unauthorized.Body.String())
	}
	for _, body := range []string{
		`{"consistency":"application_consistent","idempotency_key":"capture-1"}`,
		`{"consistency":"crash_consistent"}`,
		`{"consistency":"crash_consistent","idempotency_key":"capture-1","path":"/tmp/private"}`,
	} {
		response := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots/capture", "Bearer "+testAdminToken,
			"application/json", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid capture body %s status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	if repo.captureCalls != 0 {
		t.Fatalf("invalid capture requests reached repository %d times", repo.captureCalls)
	}
	captured := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots/capture", "Bearer "+testAdminToken,
		"application/json", `{"consistency":"crash_consistent","idempotency_key":"capture-1"}`)
	if captured.Code != http.StatusAccepted || !strings.Contains(captured.Header().Get("Location"), "/operations/op_capture") {
		t.Fatalf("capture status=%d location=%q body=%s", captured.Code, captured.Header().Get("Location"), captured.Body.String())
	}
	var captureResponse v1.SnapshotOperationResponse
	if err := json.Unmarshal(captured.Body.Bytes(), &captureResponse); err != nil {
		t.Fatal(err)
	}
	if captureResponse.Snapshot.CaptureStatus != "queued" || captureResponse.Operation.State != v1.OperationQueued || captureResponse.Snapshot.RestoreStatus != "not_requested" {
		t.Fatalf("capture response has incorrect lifecycle state: %+v", captureResponse)
	}
	if strings.Contains(captured.Body.String(), "/tmp/") || strings.Contains(captured.Body.String(), "firecracker.sock") {
		t.Fatalf("capture response leaked a local path or socket: %s", captured.Body.String())
	}

	badRestore := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots/snp_a/restore", "Bearer "+testAdminToken,
		"application/json", `{"consistency":"crash_consistent","idempotency_key":"restore-1"}`)
	if badRestore.Code != http.StatusBadRequest || repo.restoreCalls != 0 {
		t.Fatalf("restore with client-supplied consistency status=%d calls=%d body=%s", badRestore.Code, repo.restoreCalls, badRestore.Body.String())
	}
	restored := serve(server, http.MethodPost, "/api/v1/instances/ins_a/snapshots/snp_a/restore", "Bearer "+testAdminToken,
		"application/json", `{"idempotency_key":"restore-1"}`)
	if restored.Code != http.StatusAccepted || !strings.Contains(restored.Header().Get("Location"), "/operations/op_restore") {
		t.Fatalf("restore status=%d location=%q body=%s", restored.Code, restored.Header().Get("Location"), restored.Body.String())
	}
	var restoreResponse v1.SnapshotOperationResponse
	if err := json.Unmarshal(restored.Body.Bytes(), &restoreResponse); err != nil {
		t.Fatal(err)
	}
	if restoreResponse.Snapshot.RestoreStatus != "queued" || restoreResponse.Operation.State != v1.OperationQueued {
		t.Fatalf("restore response has incorrect lifecycle state: %+v", restoreResponse)
	}
}

func int64Ptr(value int64) *int64 { return &value }
