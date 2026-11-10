package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresResourceStoreIntegration is destructive only inside a database
// whose name starts with porter_test_store_. Set PORTER_STORE_TEST_POSTGRES_DSN
// to an explicitly disposable PostgreSQL database to enable it.
func TestPostgresResourceStoreIntegration(t *testing.T) {
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
	for _, statement := range []string{
		`CREATE FUNCTION porter_control_plane.reject_test_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test event rejection'; END $$`,
		`CREATE TRIGGER reject_test_event BEFORE INSERT ON porter_control_plane.resource_events FOR EACH ROW EXECUTE FUNCTION porter_control_plane.reject_test_event()`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("install event rollback probe: %v", err)
		}
	}
	if _, err := repo.CreateProject(ctx, "must roll back with event"); err == nil {
		t.Fatal("project creation succeeded despite rejected event insertion")
	}
	var rolledBackProjects int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.projects WHERE name = 'must roll back with event'`).Scan(&rolledBackProjects); err != nil || rolledBackProjects != 0 {
		t.Fatalf("resource transaction was not rolled back with event: count=%d err=%v", rolledBackProjects, err)
	}
	for _, statement := range []string{
		`DROP TRIGGER reject_test_event ON porter_control_plane.resource_events`,
		`DROP FUNCTION porter_control_plane.reject_test_event()`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("remove event rollback probe: %v", err)
		}
	}

	project, err := repo.CreateProject(ctx, "  demo project  ")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if project.Name != "demo project" || project.State != v1.ProjectActive || project.ID == "" {
		t.Fatalf("created project = %+v", project)
	}
	gotProject, err := repo.GetProject(ctx, project.ID)
	if err != nil || gotProject.ID != project.ID || gotProject.Name != project.Name || gotProject.State != project.State ||
		!gotProject.CreatedAt.Equal(project.CreatedAt) || !gotProject.UpdatedAt.Equal(project.UpdatedAt) {
		t.Fatalf("get project = %+v, %v; want %+v", gotProject, err, project)
	}
	projects, err := repo.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("list projects = %+v, %v", projects, err)
	}

	service, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "  web  "})
	if err != nil {
		t.Fatalf("create service with defaults: %v", err)
	}
	if service.Name != "web" || service.Runtime != v1.FirecrackerRuntime || service.Port != 8080 ||
		service.VCPUs != 1 || service.MemoryMB != 512 || service.DesiredInstances != 1 {
		t.Fatalf("service defaults not applied: %+v", service)
	}
	gotService, err := repo.GetService(ctx, service.ID)
	if err != nil || gotService.ID != service.ID || gotService.ProjectID != service.ProjectID ||
		gotService.Name != service.Name || gotService.State != service.State || gotService.Runtime != service.Runtime ||
		gotService.Port != service.Port || gotService.VCPUs != service.VCPUs || gotService.MemoryMB != service.MemoryMB ||
		gotService.DesiredInstances != service.DesiredInstances ||
		!gotService.CreatedAt.Equal(service.CreatedAt) || !gotService.UpdatedAt.Equal(service.UpdatedAt) {
		t.Fatalf("get service = %+v, %v; want %+v", gotService, err, service)
	}
	services, err := repo.ListServices(ctx, project.ID)
	if err != nil || len(services) != 1 || services[0].ID != service.ID {
		t.Fatalf("list services = %+v, %v", services, err)
	}
	if _, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "docker", Runtime: "docker"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("non-Firecracker runtime error = %v, want ErrInvalidArgument", err)
	}
	if err := repo.SetDesiredInstances(ctx, service.ID, 0); err != nil {
		t.Fatalf("set desired instances to zero: %v", err)
	}
	if err := repo.SetDesiredInstances(ctx, service.ID, 1); err != nil {
		t.Fatalf("restore desired instances: %v", err)
	}

	otherProject, err := repo.CreateProject(ctx, "other project")
	if err != nil {
		t.Fatalf("create second project for domain scope test: %v", err)
	}
	otherService, err := repo.CreateService(ctx, otherProject.ID, v1.CreateServiceRequest{Name: "other service"})
	if err != nil {
		t.Fatalf("create second service for domain scope test: %v", err)
	}
	claim, err := repo.CreateDomainClaim(ctx, project.ID, service.ID, " API.Example.COM. ")
	if err != nil {
		t.Fatalf("create domain claim: %v", err)
	}
	if claim.Hostname != "api.example.com" || claim.State != v1.DomainClaimPendingProof ||
		claim.VerificationTXTName != "_porter-verify.api.example.com" || !strings.HasPrefix(claim.VerificationTXTValue, "porter-verify=") ||
		claim.OwnershipProofTXTValue != "" || claim.OwnershipProvedAt != nil {
		t.Fatalf("initial domain claim = %+v", claim)
	}
	otherClaim, err := repo.CreateDomainClaim(ctx, otherProject.ID, otherService.ID, "other.example.com")
	if err != nil {
		t.Fatalf("create claim in second project: %v", err)
	}
	if otherClaim.ProjectID != otherProject.ID || otherClaim.ServiceID != otherService.ID {
		t.Fatalf("second project claim has incorrect scope: %+v", otherClaim)
	}
	if _, err := repo.CreateDomainClaim(ctx, project.ID, otherService.ID, "mismatch.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project claim creation error = %v, want ErrNotFound", err)
	}
	scopedClaims, err := repo.ListDomainClaimsPage(ctx, project.ID, service.ID, 10, nil)
	if err != nil || len(scopedClaims) != 1 || scopedClaims[0].ID != claim.ID {
		t.Fatalf("project/service scoped claims = %+v, %v", scopedClaims, err)
	}
	otherScopedClaims, err := repo.ListDomainClaimsPage(ctx, project.ID, otherService.ID, 10, nil)
	if err != nil || len(otherScopedClaims) != 0 {
		t.Fatalf("mismatched project/service claim list = %+v, %v", otherScopedClaims, err)
	}
	allClaims, err := repo.ListDomainClaimsPage(ctx, "", "", 10, nil)
	if err != nil || len(allClaims) != 2 {
		t.Fatalf("global domain claims = %+v, %v", allClaims, err)
	}

	route, err := repo.CreateGatewayRoute(ctx, claim.ID, "/")
	if err != nil {
		t.Fatalf("create preview route intent: %v", err)
	}
	if route.DomainClaimID != claim.ID || route.ProjectID != project.ID || route.ServiceID != service.ID ||
		route.Hostname != claim.Hostname || route.DesiredEnabled || route.GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("initial gateway route = %+v", route)
	}
	if _, err := repo.SetGatewayRouteDesiredEnabled(ctx, route.ID, true); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("route intent before proof error = %v, want ErrFailedPrecondition", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.gateway_routes SET desired_enabled = TRUE WHERE id = $1`, route.ID); err == nil {
		t.Fatal("database trigger allowed route intent before persisted TXT proof")
	}
	if _, err := repo.RecordDomainTXTProof(ctx, claim.ID, "porter-verify="+strings.Repeat("f", 64)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("mismatched TXT proof error = %v, want ErrFailedPrecondition", err)
	}
	claim, err = repo.GetDomainClaim(ctx, claim.ID)
	if err != nil || claim.State != v1.DomainClaimPendingProof || claim.OwnershipProofTXTValue != "" || claim.OwnershipProvedAt != nil {
		t.Fatalf("mismatched proof changed claim: %+v, %v", claim, err)
	}
	claim, err = repo.RecordDomainTXTProof(ctx, claim.ID, claim.VerificationTXTValue)
	if err != nil || claim.State != v1.DomainClaimVerified || claim.OwnershipProofTXTValue != claim.VerificationTXTValue || claim.OwnershipProvedAt == nil {
		t.Fatalf("persisted TXT proof = %+v, %v", claim, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.domain_claims SET ownership_proof_txt_value = $2 WHERE id = $1`, claim.ID, "porter-verify="+strings.Repeat("e", 64)); err == nil {
		t.Fatal("database trigger allowed persisted ownership proof to be rewritten")
	}
	route, err = repo.SetGatewayRouteDesiredEnabled(ctx, route.ID, true)
	if err != nil || !route.DesiredEnabled || route.GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("route intent after proof = %+v, %v", route, err)
	}
	routes, err := repo.ListGatewayRoutesPage(ctx, claim.ID, 10, nil)
	if err != nil || len(routes) != 1 || routes[0].ID != route.ID || !routes[0].DesiredEnabled {
		t.Fatalf("gateway route page = %+v, %v", routes, err)
	}
	if got, err := repo.GetGatewayRoute(ctx, route.ID); err != nil || got.Hostname != claim.Hostname || got.GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("get gateway route = %+v, %v", got, err)
	}
	var domainEvent, routeEvent int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE domain_claim_id = $1`, claim.ID).Scan(&domainEvent); err != nil || domainEvent != 4 {
		t.Fatalf("domain event references = %d, err=%v; want claim-created, verified, and route-history references", domainEvent, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.resource_events WHERE gateway_route_id = $1`, route.ID).Scan(&routeEvent); err != nil || routeEvent != 2 {
		t.Fatalf("route event references = %d, err=%v; want route-created and intent-changed events", routeEvent, err)
	}

	first, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main"))
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	if first.Sequence != 1 || first.State != v1.ReleaseQueued || first.Intent != v1.ReleaseIntentPreview || first.Source.Ref != "main" || first.ResolvedCommit != "" {
		t.Fatalf("created release = %+v", first)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET deployment_intent = 'production' WHERE id = $1`, first.ID); err == nil {
		t.Fatal("direct SQL rewrote immutable release environment intent")
	}
	if _, err := repo.GetRelease(ctx, first.ID); err != nil {
		t.Fatalf("get release: %v", err)
	}
	if releases, err := repo.ListReleases(ctx, service.ID); err != nil || len(releases) != 1 || releases[0].ID != first.ID {
		t.Fatalf("list releases = %+v, %v", releases, err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseBuilding); err != nil {
		t.Fatalf("start release build state: %v", err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseReady); err == nil {
		t.Fatal("release became ready without a pinned commit and artifact digest")
	}
	commit := strings.Repeat("a", 40)
	if err := repo.PinReleaseCommit(ctx, first.ID, commit); err != nil {
		t.Fatalf("pin resolved commit: %v", err)
	}
	if err := repo.PinReleaseCommit(ctx, first.ID, strings.Repeat("b", 40)); err == nil {
		t.Fatal("release source commit changed after being pinned")
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	if err := repo.SetReleaseArtifactDigest(ctx, first.ID, digest); err != nil {
		t.Fatalf("set artifact digest: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET state = 'ready' WHERE id = $1`, first.ID); err == nil {
		t.Fatal("direct SQL bypassed the guest-ready requirement")
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET state = 'ready', guest_ready_digest = artifact_digest, guest_ready_at = now() WHERE id = $1`, first.ID); err == nil {
		t.Fatal("same-statement SQL guest-ready proof bypassed the persisted receipt requirement")
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET state = 'superseded' WHERE id = $1`, first.ID); err == nil {
		t.Fatal("direct SQL moved an unready release to superseded")
	}
	if err := repo.MarkReleaseGuestReady(ctx, first.ID, "sha256:"+strings.Repeat("d", 64)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("mismatched guest-ready digest error=%v, want ErrFailedPrecondition", err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseReady); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("release ready without guest-boot receipt error=%v, want ErrFailedPrecondition", err)
	}
	if err := repo.MarkReleaseGuestReady(ctx, first.ID, digest); err != nil {
		t.Fatalf("record guest-ready receipt: %v", err)
	}
	if err := repo.MarkReleaseGuestReady(ctx, first.ID, digest); err != nil {
		t.Fatalf("repeat matching guest-ready receipt: %v", err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseReady); err != nil {
		t.Fatalf("mark release ready: %v", err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseDeploying); err != nil {
		t.Fatalf("start release deployment state: %v", err)
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseActive); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("activate release before health-gate pass error=%v, want ErrFailedPrecondition", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET state = 'active' WHERE id = $1`, first.ID); err == nil {
		t.Fatal("direct SQL bypassed the health-gate requirement")
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET state = 'active', health_gate_status = 'passed', health_gate_digest = artifact_digest, health_gate_checked_at = now() WHERE id = $1`, first.ID); err == nil {
		t.Fatal("same-statement SQL health-gate proof bypassed the persisted result requirement")
	}
	if err := repo.RecordReleaseHealthGate(ctx, first.ID, "sha256:"+strings.Repeat("d", 64), true); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("mismatched health-gate digest error=%v, want ErrFailedPrecondition", err)
	}
	if err := repo.RecordReleaseHealthGate(ctx, first.ID, digest, true); err != nil {
		t.Fatalf("record passing deployment health gate: %v", err)
	}
	if err := repo.RecordReleaseHealthGate(ctx, first.ID, digest, true); err != nil {
		t.Fatalf("repeat matching health-gate result: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.releases SET health_gate_status = 'failed', health_gate_error = 'changed', health_gate_checked_at = now() WHERE id = $1`, first.ID); err == nil {
		t.Fatal("direct SQL changed immutable passing health-gate evidence")
	}
	if err := repo.TransitionRelease(ctx, first.ID, v1.ReleaseActive); err != nil {
		t.Fatalf("activate first release: %v", err)
	}
	first, err = repo.GetRelease(ctx, first.ID)
	if err != nil || first.State != v1.ReleaseActive || first.ResolvedCommit != commit || first.ArtifactDigest != digest {
		t.Fatalf("active release = %+v, %v", first, err)
	}

	instance, err := repo.CreateInstance(ctx, service.ID, first.ID, 1)
	if err != nil {
		t.Fatalf("create instance record: %v", err)
	}
	if instance.Runtime != v1.FirecrackerRuntime || instance.State != v1.InstancePending || instance.Health != v1.HealthUnknown || instance.NodeID != "" {
		t.Fatalf("initial instance metadata = %+v", instance)
	}
	node, err := repo.CreateFleetNode(ctx, "test-node")
	if err != nil || node.State != v1.FleetNodeOffline || node.LastSeenAt != nil {
		t.Fatalf("create offline Fleet-node record = %+v, %v", node, err)
	}
	if err := repo.SetInstanceNode(ctx, instance.ID, node.ID); err != nil {
		t.Fatalf("assign instance node: %v", err)
	}
	if got, err := repo.GetInstance(ctx, instance.ID); err != nil || got.NodeID != node.ID {
		t.Fatalf("instance node assignment = %+v, %v", got, err)
	}
	if err := repo.SetInstanceNode(ctx, instance.ID, ""); err != nil {
		t.Fatalf("clear instance node: %v", err)
	}
	if err := repo.SetInstanceState(ctx, instance.ID, v1.InstanceRunning, v1.HealthHealthy); err != nil {
		t.Fatalf("update instance state: %v", err)
	}
	gotInstance, err := repo.GetInstance(ctx, instance.ID)
	if err != nil || gotInstance.State != v1.InstanceRunning || gotInstance.Health != v1.HealthHealthy || gotInstance.NodeID != "" {
		t.Fatalf("get updated instance = %+v, %v", gotInstance, err)
	}
	instances, err := repo.ListInstances(ctx, first.ID)
	if err != nil || len(instances) != 1 || instances[0].ID != instance.ID {
		t.Fatalf("list instances = %+v, %v", instances, err)
	}
	if _, err := repo.CreateInstance(ctx, service.ID, first.ID, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero ordinal error = %v, want ErrInvalidArgument", err)
	}

	if _, err := repo.CreateVolume(ctx, otherProject.ID, service.ID, v1.CreateVolumeRequest{Name: "wrong-scope", SizeMiB: 512}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project volume creation error = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateVolume(ctx, project.ID, service.ID, v1.CreateVolumeRequest{Name: "shared", SizeMiB: 512, AccessMode: "shared_writable"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("shared-writable volume creation error = %v, want ErrInvalidArgument", err)
	}
	volume, err := repo.CreateVolume(ctx, project.ID, service.ID, v1.CreateVolumeRequest{
		Name: "durable-data", SizeMiB: 2048, MountPath: "/data", AccessMode: v1.VolumeSingleWriter, NodeAffinityID: node.ID,
	})
	if err != nil {
		t.Fatalf("create volume metadata: %v", err)
	}
	if volume.ProjectID != project.ID || volume.ServiceID != service.ID || volume.State != v1.VolumeMetadataOnly ||
		volume.AccessMode != v1.VolumeSingleWriter || volume.IntegrityStatus != v1.IntegrityNotAvailable ||
		volume.NodeAffinityID == nil || *volume.NodeAffinityID != node.ID {
		t.Fatalf("created volume metadata = %+v", volume)
	}
	gotVolume, err := repo.GetVolume(ctx, volume.ID)
	if err != nil || gotVolume.ID != volume.ID || gotVolume.MountPath != "/data" || gotVolume.NodeAffinityID == nil || *gotVolume.NodeAffinityID != node.ID {
		t.Fatalf("get volume metadata = %+v, %v", gotVolume, err)
	}
	volumePage, err := repo.ListVolumesPage(ctx, project.ID, service.ID, 10, nil)
	if err != nil || len(volumePage) != 1 || volumePage[0].ID != volume.ID {
		t.Fatalf("list volume metadata = %+v, %v", volumePage, err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO porter_control_plane.volume_metadata (id, project_id, service_id, name, size_mib, access_mode)
VALUES ('vol_rejected_shared', $1, $2, 'bad-shared', 512, 'shared_writable')`, project.ID, service.ID); err == nil {
		t.Fatal("database accepted unsupported shared-writable volume metadata")
	}

	if err := repo.SetInstanceNode(ctx, instance.ID, node.ID); err != nil {
		t.Fatalf("restore instance node affinity for metadata test: %v", err)
	}
	projectInstances, err := repo.ListProjectInstancesPage(ctx, project.ID, 10, nil)
	if err != nil || len(projectInstances) != 1 || projectInstances[0].ID != instance.ID {
		t.Fatalf("list project instances = %+v, %v", projectInstances, err)
	}
	if _, err := repo.CreateSnapshotMetadata(ctx, instance.ID, "application_consistent"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("application-consistent snapshot error = %v, want ErrInvalidArgument", err)
	}
	snapshot, err := repo.CreateSnapshotMetadata(ctx, instance.ID, v1.SnapshotCrashConsistent)
	if err != nil {
		t.Fatalf("create snapshot metadata: %v", err)
	}
	if snapshot.ProjectID != project.ID || snapshot.ServiceID != service.ID || snapshot.InstanceID != instance.ID ||
		snapshot.Consistency != v1.SnapshotCrashConsistent || snapshot.CaptureStatus != v1.SnapshotCaptureUnavailable ||
		snapshot.IntegrityStatus != v1.IntegrityNotAvailable || snapshot.ArtifactSHA256 != nil || snapshot.SizeBytes != nil ||
		snapshot.NodeAffinityID == nil || *snapshot.NodeAffinityID != node.ID {
		t.Fatalf("snapshot metadata claims capture or lost scope/affinity: %+v", snapshot)
	}
	gotSnapshot, err := repo.GetSnapshotMetadata(ctx, snapshot.ID)
	if err != nil || gotSnapshot.ID != snapshot.ID || gotSnapshot.InstanceID != instance.ID {
		t.Fatalf("get snapshot metadata = %+v, %v", gotSnapshot, err)
	}
	snapshotPage, err := repo.ListSnapshotMetadataPage(ctx, project.ID, instance.ID, 10, nil)
	if err != nil || len(snapshotPage) != 1 || snapshotPage[0].ID != snapshot.ID {
		t.Fatalf("list snapshot metadata = %+v, %v", snapshotPage, err)
	}
	if _, err := repo.CreateBackupMetadata(ctx, "missing-snapshot", v1.SnapshotCrashConsistent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("backup for missing snapshot error = %v, want ErrNotFound", err)
	}
	if _, err := repo.CreateBackupMetadata(ctx, snapshot.ID, "application_consistent"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("application-consistent backup error = %v, want ErrInvalidArgument", err)
	}
	backup, err := repo.CreateBackupMetadata(ctx, snapshot.ID, v1.SnapshotCrashConsistent)
	if err != nil {
		t.Fatalf("create backup metadata: %v", err)
	}
	if backup.ProjectID != project.ID || backup.ServiceID != service.ID || backup.InstanceID != instance.ID || backup.SnapshotID != snapshot.ID ||
		backup.Consistency != v1.SnapshotCrashConsistent || backup.State != v1.BackupMetadataOnly ||
		backup.OffHostStatus != v1.BackupOffHostUnavailable || backup.IntegrityStatus != v1.IntegrityNotAvailable ||
		backup.ArtifactSHA256 != nil || backup.SizeBytes != nil || backup.NodeAffinityID == nil || *backup.NodeAffinityID != node.ID {
		t.Fatalf("backup metadata claims off-host data or lost scope/affinity: %+v", backup)
	}
	gotBackup, err := repo.GetBackupMetadata(ctx, backup.ID)
	if err != nil || gotBackup.ID != backup.ID || gotBackup.SnapshotID != snapshot.ID {
		t.Fatalf("get backup metadata = %+v, %v", gotBackup, err)
	}
	backupPage, err := repo.ListBackupMetadataPage(ctx, project.ID, snapshot.ID, 10, nil)
	if err != nil || len(backupPage) != 1 || backupPage[0].ID != backup.ID {
		t.Fatalf("list backup metadata = %+v, %v", backupPage, err)
	}

	const concurrentReleases = 4
	var wait sync.WaitGroup
	created := make(chan v1.Release, concurrentReleases)
	createErrors := make(chan error, concurrentReleases)
	for i := 0; i < concurrentReleases; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			release, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main"))
			if err != nil {
				createErrors <- err
				return
			}
			created <- release
		}()
	}
	wait.Wait()
	close(created)
	close(createErrors)
	for err := range createErrors {
		t.Fatalf("concurrent release creation: %v", err)
	}
	sequences := make(map[int64]bool, concurrentReleases)
	for release := range created {
		sequences[release.Sequence] = true
	}
	for sequence := int64(2); sequence <= concurrentReleases+1; sequence++ {
		if !sequences[sequence] {
			t.Fatalf("concurrent release sequences missing %d: %v", sequence, sequences)
		}
	}

	candidate, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main"))
	if err != nil {
		t.Fatalf("create candidate release: %v", err)
	}
	if err := repo.TransitionRelease(ctx, candidate.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repo.PinReleaseCommit(ctx, candidate.ID, strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetReleaseArtifactDigest(ctx, candidate.ID, "sha256:"+strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	candidateDigest := "sha256:" + strings.Repeat("e", 64)
	if err := repo.MarkReleaseGuestReady(ctx, candidate.ID, candidateDigest); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, candidate.ID, v1.ReleaseReady); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, candidate.ID, v1.ReleaseDeploying); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordReleaseHealthGate(ctx, candidate.ID, candidateDigest, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetServiceState(ctx, service.ID, v1.ServiceDeleting); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, candidate.ID, v1.ReleaseActive); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("promotion for deleting service error = %v, want ErrFailedPrecondition", err)
	}
	first, err = repo.GetRelease(ctx, first.ID)
	if err != nil || first.State != v1.ReleaseActive {
		t.Fatalf("failed promotion changed prior active release: %+v, %v", first, err)
	}
	candidate, err = repo.GetRelease(ctx, candidate.ID)
	if err != nil || candidate.State != v1.ReleaseDeploying {
		t.Fatalf("failed promotion changed candidate state: %+v, %v", candidate, err)
	}
	if err := repo.SetServiceState(ctx, service.ID, v1.ServiceActive); err != nil {
		t.Fatal(err)
	}
	if err := repo.TransitionRelease(ctx, candidate.ID, v1.ReleaseActive); err != nil {
		t.Fatalf("promote candidate release: %v", err)
	}
	first, err = repo.GetRelease(ctx, first.ID)
	if err != nil || first.State != v1.ReleaseSuperseded {
		t.Fatalf("prior active release = %+v, %v; want superseded", first, err)
	}
	candidate, err = repo.GetRelease(ctx, candidate.ID)
	if err != nil || candidate.State != v1.ReleaseActive {
		t.Fatalf("candidate release = %+v, %v; want active", candidate, err)
	}
	readyPromotion, err := repo.RequestProductionPromotion(ctx, candidate.ID, "active-candidate-promotion")
	if err != nil {
		t.Fatalf("record production promotion checks for fully ready release: %v", err)
	}
	if readyPromotion.Blocker != "production_activation_worker" || len(readyPromotion.Checks) != 6 {
		t.Fatalf("fully evidenced promotion should be blocked only by missing worker: %+v", readyPromotion)
	}
	for _, check := range readyPromotion.Checks[:5] {
		if !check.Passed {
			t.Fatalf("promotion evidence check %q should pass for the active digest-matched release", check.Name)
		}
	}
	if readyPromotion.Checks[5].Passed {
		t.Fatal("promotion must not pass while the production activation worker is unavailable")
	}
	listedReleases, err := repo.ListReleases(ctx, service.ID)
	if err != nil || len(listedReleases) != concurrentReleases+2 || listedReleases[0].ID != candidate.ID {
		t.Fatalf("release history = %+v, %v", listedReleases, err)
	}

	intentRequest := releaseRequest("main")
	intentRequest.Intent = v1.ReleaseIntentProduction
	blockedRelease, buildOperation, err := repo.CreateBlockedReleaseIntent(ctx, service.ID, intentRequest, "source-intent-once")
	if err != nil {
		t.Fatalf("persist blocked production release intent: %v", err)
	}
	if blockedRelease.State != v1.ReleaseBlocked || blockedRelease.Intent != v1.ReleaseIntentProduction || blockedRelease.ResolvedCommit != "" || blockedRelease.ArtifactDigest != "" ||
		buildOperation.State != v1.ReleaseOperationBlocked || buildOperation.Blocker != "builder_unavailable" || buildOperation.RequestedRef != "main" {
		t.Fatalf("blocked release intent claimed unearned build progress: release=%+v operation=%+v", blockedRelease, buildOperation)
	}
	replayedRelease, replayedOperation, err := repo.CreateBlockedReleaseIntent(ctx, service.ID, intentRequest, "source-intent-once")
	if err != nil || replayedRelease.ID != blockedRelease.ID || replayedOperation.ID != buildOperation.ID {
		t.Fatalf("release-intent replay = release %s operation %s err %v; want same durable IDs", replayedRelease.ID, replayedOperation.ID, err)
	}
	changedRequest := intentRequest
	changedRequest.Source.Ref = "main-next"
	if _, _, err := repo.CreateBlockedReleaseIntent(ctx, service.ID, changedRequest, "source-intent-once"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request with reused idempotency key error=%v, want ErrIdempotencyConflict", err)
	}
	promotion, err := repo.RequestProductionPromotion(ctx, blockedRelease.ID, "production-check-once")
	if err != nil {
		t.Fatalf("record blocked production promotion checks: %v", err)
	}
	if promotion.State != v1.ReleaseOperationBlocked || promotion.Blocker != "immutable_commit_pinned" || len(promotion.Checks) != 6 || promotion.Checks[0].Passed || promotion.Checks[len(promotion.Checks)-1].Passed {
		t.Fatalf("promotion check incorrectly passed without source/build/worker evidence: %+v", promotion)
	}
	replayedPromotion, err := repo.RequestProductionPromotion(ctx, blockedRelease.ID, "production-check-once")
	if err != nil || replayedPromotion.ID != promotion.ID {
		t.Fatalf("promotion replay = %+v err %v; want same operation", replayedPromotion, err)
	}
	operationHistory, err := repo.ListReleaseOperations(ctx, blockedRelease.ID, 100)
	if err != nil || len(operationHistory) != 2 {
		t.Fatalf("release operation history = %+v, %v; want build and promotion records", operationHistory, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.release_operations SET blocker = 'changed' WHERE id = $1`, buildOperation.ID); err == nil {
		t.Fatal("release operation record was mutable; operation history must be append-only")
	}

	if err := repo.SetServiceState(ctx, service.ID, v1.ServiceDeleting); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRelease(ctx, service.ID, releaseRequest("main")); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("release on deleting service error = %v, want ErrFailedPrecondition", err)
	}
	if err := repo.SetServiceState(ctx, service.ID, v1.ServiceActive); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetProjectState(ctx, project.ID, v1.ProjectDeleting); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateService(ctx, project.ID, v1.CreateServiceRequest{Name: "blocked"}); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("service on deleting project error = %v, want ErrFailedPrecondition", err)
	}
	if _, err := repo.GetProject(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project error = %v, want ErrNotFound", err)
	}

	nodes, err := repo.ListFleetNodesPage(ctx, 10, nil)
	if err != nil || len(nodes) != 1 || nodes[0].ID != node.ID || nodes[0].LastSeenAt != nil {
		t.Fatalf("list Fleet-node page = %+v, %v", nodes, err)
	}
	events, err := repo.ListEventsPage(ctx, 100, nil)
	if err != nil || len(events) == 0 {
		t.Fatalf("list durable events = %d events, %v", len(events), err)
	}
	seen := make(map[string]bool, len(events))
	for i, event := range events {
		seen[event.Type] = true
		if i > 0 && (event.OccurredAt.After(events[i-1].OccurredAt) || (event.OccurredAt.Equal(events[i-1].OccurredAt) && event.ID > events[i-1].ID)) {
			t.Fatalf("event order is not descending: previous=%+v current=%+v", events[i-1], event)
		}
	}
	for _, eventType := range []string{"project.created", "project.state_changed", "service.created", "service.state_changed", "service.desired_instances_changed", "release.created", "release.intent_recorded", "release.production_promotion_blocked", "release.commit_pinned", "release.artifact_digest_set", "release.state_changed", "instance.created", "instance.state_changed", "instance.node_changed", "fleet_node.created", "domain_claim.created", "domain_claim.verified", "gateway_route.created", "gateway_route.desired_state_changed", "volume.metadata_created", "snapshot.metadata_created", "backup.metadata_created"} {
		if !seen[eventType] {
			t.Errorf("durable events missing %q; saw %v", eventType, seen)
		}
	}
	var volumeEvent, snapshotEvent, backupEvent bool
	for _, event := range events {
		if event.VolumeID == volume.ID && event.Type == "volume.metadata_created" {
			volumeEvent = true
		}
		if event.SnapshotID == snapshot.ID && event.Type == "snapshot.metadata_created" {
			snapshotEvent = true
		}
		if event.BackupID == backup.ID && event.SnapshotID == snapshot.ID && event.Type == "backup.metadata_created" {
			backupEvent = true
		}
	}
	if !volumeEvent || !snapshotEvent || !backupEvent {
		t.Fatalf("storage metadata events lost references: volume=%v snapshot=%v backup=%v", volumeEvent, snapshotEvent, backupEvent)
	}
	firstEvent := events[0]
	if _, err := db.ExecContext(ctx, `UPDATE porter_control_plane.resource_events SET data = '{}'::jsonb WHERE id = $1`, firstEvent.ID); err == nil {
		t.Fatal("resource event update was allowed; event history must be append-only")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM porter_control_plane.resource_events WHERE id = $1`, firstEvent.ID); err == nil {
		t.Fatal("resource event delete was allowed; event history must be append-only")
	}
}

func releaseRequest(ref string) v1.CreateReleaseRequest {
	return v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: "https://example.test/source.git", Ref: ref},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "Dockerfile"},
	}
}

func resetStoreIntegrationObjects(ctx context.Context, db *sql.DB) error {
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
