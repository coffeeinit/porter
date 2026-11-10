package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	"porter/controlplane/internal/postgres/migrations"
	pgstore "porter/controlplane/internal/postgres/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresHTTPAPIIntegration uses the real PostgreSQL store behind the
// isolated HTTP adapter. Set PORTER_HTTPAPI_TEST_POSTGRES_DSN to an explicitly
// disposable database named porter_test_httpapi_* to enable it.
func TestPostgresHTTPAPIIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PORTER_HTTPAPI_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set PORTER_HTTPAPI_TEST_POSTGRES_DSN to a disposable porter_test_httpapi_* database")
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
	if !strings.HasPrefix(databaseName, "porter_test_httpapi_") {
		t.Fatalf("refusing destructive integration test in database %q; name must start with porter_test_httpapi_", databaseName)
	}
	if err := resetHTTPAPIIntegrationDB(ctx, db); err != nil {
		t.Fatalf("reset disposable integration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := resetHTTPAPIIntegrationDB(cleanupCtx, db); err != nil {
			t.Errorf("clean up disposable integration database: %v", err)
		}
	})
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply PostgreSQL schema: %v", err)
	}
	repository, err := pgstore.New(db)
	if err != nil {
		t.Fatal(err)
	}
	txtResolver := &integrationTXTResolver{records: make(map[string][]string)}
	adapter, err := NewWithTXTResolver(repository, "integration-operator-token", txtResolver)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(adapter)
	defer server.Close()

	var createdProjects []v1.Project
	for i, name := range []string{"api-one", "api-two", "api-three"} {
		var project v1.Project
		response := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+v1.BasePath+"/projects", "integration-operator-token", v1.CreateProjectRequest{Name: name}, &project)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create project %d: status=%d body=%s", i, response.StatusCode, readBody(t, response))
		}
		if got := response.Header.Get("Location"); got != v1.BasePath+"/projects/"+project.ID {
			t.Fatalf("project Location = %q, ID=%q", got, project.ID)
		}
		response.Body.Close()
		createdProjects = append(createdProjects, project)
	}
	pagedProjects := collectPages[v1.Project](t, server.Client(), server.URL, v1.BasePath+"/projects", "integration-operator-token")
	assertSameIDs(t, projectIDs(createdProjects), projectIDs(pagedProjects))
	projectResponse, err := doAPIRequest(server.Client(), http.MethodGet, server.URL+v1.BasePath+"/projects/"+url.PathEscape(createdProjects[0].ID), "integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var fetchedProject v1.Project
	if projectResponse.StatusCode != http.StatusOK {
		t.Fatalf("get project status=%d body=%s", projectResponse.StatusCode, readBody(t, projectResponse))
	}
	if err := json.NewDecoder(projectResponse.Body).Decode(&fetchedProject); err != nil {
		t.Fatal(err)
	}
	projectResponse.Body.Close()
	if fetchedProject.ID != createdProjects[0].ID {
		t.Fatalf("fetched project = %+v", fetchedProject)
	}

	projectID := createdProjects[0].ID
	var createdServices []v1.Service
	for _, name := range []string{"web", "worker"} {
		var service v1.Service
		response := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+v1.BasePath+"/projects/"+url.PathEscape(projectID)+"/services", "integration-operator-token", v1.CreateServiceRequest{Name: name}, &service)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create service: status=%d body=%s", response.StatusCode, readBody(t, response))
		}
		response.Body.Close()
		createdServices = append(createdServices, service)
	}
	serviceResponse, err := doAPIRequest(server.Client(), http.MethodGet, server.URL+v1.BasePath+"/services/"+url.PathEscape(createdServices[0].ID), "integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var fetchedService v1.Service
	if serviceResponse.StatusCode != http.StatusOK {
		t.Fatalf("get service status=%d body=%s", serviceResponse.StatusCode, readBody(t, serviceResponse))
	}
	if err := json.NewDecoder(serviceResponse.Body).Decode(&fetchedService); err != nil {
		t.Fatal(err)
	}
	serviceResponse.Body.Close()
	if fetchedService.ID != createdServices[0].ID || fetchedService.ProjectID != projectID {
		t.Fatalf("fetched service = %+v", fetchedService)
	}
	pagedServices := collectPages[v1.Service](t, server.Client(), server.URL,
		v1.BasePath+"/projects/"+url.PathEscape(projectID)+"/services", "integration-operator-token")
	assertSameIDs(t, serviceIDs(createdServices), serviceIDs(pagedServices))
	duplicate := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+v1.BasePath+"/projects/"+url.PathEscape(projectID)+"/services", "integration-operator-token", v1.CreateServiceRequest{Name: "web"}, nil)
	if duplicate.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate service status=%d body=%s", duplicate.StatusCode, readBody(t, duplicate))
	}
	assertIntegrationError(t, duplicate, v1.ErrorConflict)

	service := createdServices[0]
	claimPath := v1.BasePath + "/projects/" + url.PathEscape(projectID) + "/services/" + url.PathEscape(service.ID) + "/domain-claims"
	var claim v1.DomainClaim
	claimResponse := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+claimPath, "integration-operator-token",
		v1.CreateDomainClaimRequest{Hostname: "Web.Example.COM."}, &claim)
	if claimResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create domain claim status=%d body=%s", claimResponse.StatusCode, readBody(t, claimResponse))
	}
	if got := claimResponse.Header.Get("Location"); got != v1.BasePath+"/domain-claims/"+claim.ID {
		t.Fatalf("domain claim Location = %q, claim ID=%q", got, claim.ID)
	}
	claimResponse.Body.Close()
	if claim.Hostname != "web.example.com" || claim.State != v1.DomainClaimPendingProof || claim.VerificationTXTName != "_porter-verify.web.example.com" {
		t.Fatalf("created claim = %+v", claim)
	}
	wrongProjectClaim := doAPIJSON(t, server.Client(), http.MethodPost,
		server.URL+v1.BasePath+"/projects/"+url.PathEscape(createdProjects[1].ID)+"/services/"+url.PathEscape(service.ID)+"/domain-claims",
		"integration-operator-token", v1.CreateDomainClaimRequest{Hostname: "wrong-scope.example.com"}, nil)
	if wrongProjectClaim.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project claim create status=%d body=%s", wrongProjectClaim.StatusCode, readBody(t, wrongProjectClaim))
	}
	assertIntegrationError(t, wrongProjectClaim, v1.ErrorNotFound)
	wrongProjectList, err := doAPIRequest(server.Client(), http.MethodGet,
		server.URL+v1.BasePath+"/projects/"+url.PathEscape(createdProjects[1].ID)+"/services/"+url.PathEscape(service.ID)+"/domain-claims",
		"integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if wrongProjectList.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project claim list status=%d body=%s", wrongProjectList.StatusCode, readBody(t, wrongProjectList))
	}
	assertIntegrationError(t, wrongProjectList, v1.ErrorNotFound)

	routePath := v1.BasePath + "/domain-claims/" + url.PathEscape(claim.ID) + "/routes"
	var route v1.GatewayRoute
	routeResponse := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+routePath, "integration-operator-token",
		v1.CreateGatewayRouteRequest{PathPrefix: "/"}, &route)
	if routeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create route intent status=%d body=%s", routeResponse.StatusCode, readBody(t, routeResponse))
	}
	routeResponse.Body.Close()
	if route.DesiredEnabled || route.GatewayStatus != v1.GatewayUnavailable || route.Hostname != claim.Hostname {
		t.Fatalf("new route must be disabled preview intent: %+v", route)
	}
	tooEarly := doAPIJSON(t, server.Client(), http.MethodPatch,
		server.URL+v1.BasePath+"/gateway-routes/"+url.PathEscape(route.ID), "integration-operator-token",
		v1.SetGatewayRouteRequest{DesiredEnabled: boolPointer(true)}, nil)
	if tooEarly.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("route enable before proof status=%d body=%s", tooEarly.StatusCode, readBody(t, tooEarly))
	}
	assertIntegrationError(t, tooEarly, v1.ErrorFailedPrecondition)
	var persistedRouteEnabled bool
	if err := db.QueryRowContext(ctx, `SELECT desired_enabled FROM porter_control_plane.gateway_routes WHERE id = $1`, route.ID).Scan(&persistedRouteEnabled); err != nil || persistedRouteEnabled {
		t.Fatalf("route intent persisted before TXT proof: enabled=%v err=%v", persistedRouteEnabled, err)
	}
	missingProof := doAPIJSON(t, server.Client(), http.MethodPost,
		server.URL+v1.BasePath+"/domain-claims/"+url.PathEscape(claim.ID)+"/verify-txt", "integration-operator-token", struct{}{}, nil)
	if missingProof.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("missing TXT record status=%d body=%s", missingProof.StatusCode, readBody(t, missingProof))
	}
	assertIntegrationError(t, missingProof, v1.ErrorFailedPrecondition)
	var proofBeforeDNS string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(ownership_proof_txt_value, '') FROM porter_control_plane.domain_claims WHERE id = $1`, claim.ID).Scan(&proofBeforeDNS); err != nil || proofBeforeDNS != "" {
		t.Fatalf("missing resolver record persisted proof %q, err=%v", proofBeforeDNS, err)
	}

	txtResolver.records[claim.VerificationTXTName+"."] = []string{claim.VerificationTXTValue}
	var verifiedClaim v1.DomainClaim
	verifyResponse := doAPIJSON(t, server.Client(), http.MethodPost,
		server.URL+v1.BasePath+"/domain-claims/"+url.PathEscape(claim.ID)+"/verify-txt", "integration-operator-token", struct{}{}, &verifiedClaim)
	if verifyResponse.StatusCode != http.StatusOK {
		t.Fatalf("verify TXT proof status=%d body=%s", verifyResponse.StatusCode, readBody(t, verifyResponse))
	}
	verifyResponse.Body.Close()
	if verifiedClaim.State != v1.DomainClaimVerified || verifiedClaim.OwnershipProofTXTValue != claim.VerificationTXTValue || verifiedClaim.OwnershipProvedAt == nil {
		t.Fatalf("TXT proof was not persisted: %+v", verifiedClaim)
	}
	var enabledRoute v1.GatewayRoute
	enableResponse := doAPIJSON(t, server.Client(), http.MethodPatch,
		server.URL+v1.BasePath+"/gateway-routes/"+url.PathEscape(route.ID), "integration-operator-token",
		v1.SetGatewayRouteRequest{DesiredEnabled: boolPointer(true)}, &enabledRoute)
	if enableResponse.StatusCode != http.StatusOK {
		t.Fatalf("route intent enable after proof status=%d body=%s", enableResponse.StatusCode, readBody(t, enableResponse))
	}
	enableResponse.Body.Close()
	if !enabledRoute.DesiredEnabled || enabledRoute.GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("API must return saved intent but never claim live gateway status: %+v", enabledRoute)
	}
	listedRoutes := collectPages[v1.GatewayRoute](t, server.Client(), server.URL, routePath, "integration-operator-token")
	if len(listedRoutes) != 1 || listedRoutes[0].ID != route.ID || !listedRoutes[0].DesiredEnabled || listedRoutes[0].GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("route pages = %+v", listedRoutes)
	}

	node, err := repository.CreateFleetNode(ctx, "integration-node")
	if err != nil {
		t.Fatalf("create Fleet-node record: %v", err)
	}
	nodeResponse, err := doAPIRequest(server.Client(), http.MethodGet, server.URL+v1.BasePath+"/fleet/nodes", "integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var nodePage v1.Page[v1.FleetNode]
	if nodeResponse.StatusCode != http.StatusOK {
		t.Fatalf("list Fleet nodes status=%d body=%s", nodeResponse.StatusCode, readBody(t, nodeResponse))
	}
	if err := json.NewDecoder(nodeResponse.Body).Decode(&nodePage); err != nil {
		t.Fatal(err)
	}
	nodeResponse.Body.Close()
	if len(nodePage.Items) != 1 || nodePage.Items[0].ID != node.ID || nodePage.Items[0].State != v1.FleetNodeOffline || nodePage.Items[0].LastSeenAt != nil {
		t.Fatalf("Fleet-node API page = %+v; want only the persisted offline node with no heartbeat", nodePage)
	}
	pagedNodes := collectPages[v1.FleetNode](t, server.Client(), server.URL, v1.BasePath+"/fleet/nodes", "integration-operator-token")
	if len(pagedNodes) != 1 || pagedNodes[0].ID != node.ID {
		t.Fatalf("Fleet-node keyset pages = %+v", pagedNodes)
	}

	validReleaseRequest := v1.CreateReleaseRequest{
		Source: v1.ReleaseSource{Kind: v1.SourceGit, Repository: "https://example.test/acme/service.git", Ref: "main"},
		Build:  v1.BuildSpec{Kind: v1.BuildDockerfile, DockerfilePath: "Dockerfile"},
	}
	var releases []v1.Release
	for i := 0; i < 3; i++ {
		release, err := repository.CreateRelease(ctx, service.ID, validReleaseRequest)
		if err != nil {
			t.Fatalf("create release fixture: %v", err)
		}
		releases = append(releases, release)
	}
	first := releases[0]
	if err := repository.TransitionRelease(ctx, first.ID, v1.ReleaseBuilding); err != nil {
		t.Fatal(err)
	}
	if err := repository.PinReleaseCommit(ctx, first.ID, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetReleaseArtifactDigest(ctx, first.ID, "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	if err := repository.MarkReleaseGuestReady(ctx, first.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := repository.TransitionRelease(ctx, first.ID, v1.ReleaseReady); err != nil {
		t.Fatal(err)
	}
	if err := repository.TransitionRelease(ctx, first.ID, v1.ReleaseDeploying); err != nil {
		t.Fatal(err)
	}
	if err := repository.TransitionRelease(ctx, first.ID, v1.ReleaseActive); err == nil {
		t.Fatal("release became active before its health gate passed")
	}
	if err := repository.RecordReleaseHealthGate(ctx, first.ID, digest, true); err != nil {
		t.Fatal(err)
	}
	if err := repository.TransitionRelease(ctx, first.ID, v1.ReleaseActive); err != nil {
		t.Fatal(err)
	}
	pagedReleases := collectPages[v1.Release](t, server.Client(), server.URL,
		v1.BasePath+"/services/"+url.PathEscape(service.ID)+"/releases", "integration-operator-token")
	if len(pagedReleases) != 3 || pagedReleases[0].Sequence != 3 || pagedReleases[1].Sequence != 2 || pagedReleases[2].Sequence != 1 {
		t.Fatalf("release page sequence order = %+v", pagedReleases)
	}
	releaseResponse, err := doAPIRequest(server.Client(), http.MethodGet, server.URL+v1.BasePath+"/releases/"+url.PathEscape(first.ID), "integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if releaseResponse.StatusCode != http.StatusOK {
		t.Fatalf("get release status=%d body=%s", releaseResponse.StatusCode, readBody(t, releaseResponse))
	}
	releaseResponse.Body.Close()

	var createdInstances []v1.Instance
	for ordinal := 1; ordinal <= 3; ordinal++ {
		instance, err := repository.CreateInstance(ctx, service.ID, first.ID, ordinal)
		if err != nil {
			t.Fatalf("create instance fixture: %v", err)
		}
		createdInstances = append(createdInstances, instance)
	}
	pagedInstances := collectPages[v1.Instance](t, server.Client(), server.URL,
		v1.BasePath+"/releases/"+url.PathEscape(first.ID)+"/instances", "integration-operator-token")
	assertSameIDs(t, instanceIDs(createdInstances), instanceIDs(pagedInstances))
	instanceResponse, err := doAPIRequest(server.Client(), http.MethodGet, server.URL+v1.BasePath+"/instances/"+url.PathEscape(createdInstances[0].ID), "integration-operator-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if instanceResponse.StatusCode != http.StatusOK {
		t.Fatalf("get instance status=%d body=%s", instanceResponse.StatusCode, readBody(t, instanceResponse))
	}
	instanceResponse.Body.Close()
	projectInstances := collectPages[v1.Instance](t, server.Client(), server.URL,
		v1.BasePath+"/projects/"+url.PathEscape(projectID)+"/instances", "integration-operator-token")
	assertSameIDs(t, instanceIDs(createdInstances), instanceIDs(projectInstances))

	volumePath := v1.BasePath + "/projects/" + url.PathEscape(projectID) + "/services/" + url.PathEscape(service.ID) + "/volumes"
	unsupportedSharedVolume := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+volumePath, "integration-operator-token",
		v1.CreateVolumeRequest{Name: "shared-volume", SizeMiB: 512, AccessMode: "shared_writable"}, nil)
	if unsupportedSharedVolume.StatusCode != http.StatusBadRequest {
		t.Fatalf("shared-writable volume request status=%d body=%s", unsupportedSharedVolume.StatusCode, readBody(t, unsupportedSharedVolume))
	}
	assertIntegrationError(t, unsupportedSharedVolume, v1.ErrorBadRequest)
	wrongScopeVolume := doAPIJSON(t, server.Client(), http.MethodPost,
		server.URL+v1.BasePath+"/projects/"+url.PathEscape(createdProjects[1].ID)+"/services/"+url.PathEscape(service.ID)+"/volumes",
		"integration-operator-token", v1.CreateVolumeRequest{Name: "wrong-scope", SizeMiB: 512, AccessMode: v1.VolumeSingleWriter}, nil)
	if wrongScopeVolume.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project volume status=%d body=%s", wrongScopeVolume.StatusCode, readBody(t, wrongScopeVolume))
	}
	assertIntegrationError(t, wrongScopeVolume, v1.ErrorNotFound)
	var volume v1.Volume
	volumeResponse := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+volumePath, "integration-operator-token",
		v1.CreateVolumeRequest{Name: "data", SizeMiB: 2048, MountPath: "/data", AccessMode: v1.VolumeSingleWriter, NodeAffinityID: node.ID}, &volume)
	if volumeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create volume metadata status=%d body=%s", volumeResponse.StatusCode, readBody(t, volumeResponse))
	}
	if got := volumeResponse.Header.Get("Location"); got != v1.BasePath+"/volumes/"+volume.ID {
		t.Fatalf("volume Location=%q, ID=%q", got, volume.ID)
	}
	volumeResponse.Body.Close()
	if volume.State != v1.VolumeMetadataOnly || volume.AccessMode != v1.VolumeSingleWriter || volume.IntegrityStatus != v1.IntegrityNotAvailable || volume.NodeAffinityID == nil || *volume.NodeAffinityID != node.ID {
		t.Fatalf("volume API response claims unsupported state or lost affinity: %+v", volume)
	}
	pagedVolumes := collectPages[v1.Volume](t, server.Client(), server.URL, volumePath, "integration-operator-token")
	if len(pagedVolumes) != 1 || pagedVolumes[0].ID != volume.ID {
		t.Fatalf("scoped volume pages=%+v", pagedVolumes)
	}
	otherProjectVolumes := collectPages[v1.Volume](t, server.Client(), server.URL,
		v1.BasePath+"/projects/"+url.PathEscape(createdProjects[1].ID)+"/volumes", "integration-operator-token")
	if len(otherProjectVolumes) != 0 {
		t.Fatalf("volume leaked across project scope: %+v", otherProjectVolumes)
	}

	if err := repository.SetInstanceNode(ctx, createdInstances[0].ID, node.ID); err != nil {
		t.Fatalf("set source instance affinity: %v", err)
	}
	instanceSnapshotsPath := v1.BasePath + "/instances/" + url.PathEscape(createdInstances[0].ID) + "/snapshots"
	unsupportedConsistency := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+instanceSnapshotsPath, "integration-operator-token",
		v1.CreateSnapshotMetadataRequest{Consistency: "application_consistent"}, nil)
	if unsupportedConsistency.StatusCode != http.StatusBadRequest {
		t.Fatalf("application-consistent snapshot status=%d body=%s", unsupportedConsistency.StatusCode, readBody(t, unsupportedConsistency))
	}
	assertIntegrationError(t, unsupportedConsistency, v1.ErrorBadRequest)
	var snapshot v1.SnapshotMetadata
	snapshotResponse := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+instanceSnapshotsPath, "integration-operator-token",
		v1.CreateSnapshotMetadataRequest{Consistency: v1.SnapshotCrashConsistent}, &snapshot)
	if snapshotResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create snapshot metadata status=%d body=%s", snapshotResponse.StatusCode, readBody(t, snapshotResponse))
	}
	snapshotResponse.Body.Close()
	if snapshot.CaptureStatus != v1.SnapshotCaptureUnavailable || snapshot.IntegrityStatus != v1.IntegrityNotAvailable || snapshot.ArtifactSHA256 != nil || snapshot.NodeAffinityID == nil || *snapshot.NodeAffinityID != node.ID {
		t.Fatalf("snapshot API response claims capture/integrity or lost node affinity: %+v", snapshot)
	}
	pagedInstanceSnapshots := collectPages[v1.SnapshotMetadata](t, server.Client(), server.URL, instanceSnapshotsPath, "integration-operator-token")
	if len(pagedInstanceSnapshots) != 1 || pagedInstanceSnapshots[0].ID != snapshot.ID {
		t.Fatalf("instance snapshot pages=%+v", pagedInstanceSnapshots)
	}
	backupPath := v1.BasePath + "/snapshots/" + url.PathEscape(snapshot.ID) + "/backup-records"
	var backup v1.BackupMetadata
	backupResponse := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+backupPath, "integration-operator-token",
		v1.CreateBackupMetadataRequest{Consistency: v1.SnapshotCrashConsistent}, &backup)
	if backupResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create backup metadata status=%d body=%s", backupResponse.StatusCode, readBody(t, backupResponse))
	}
	backupResponse.Body.Close()
	if backup.State != v1.BackupMetadataOnly || backup.OffHostStatus != v1.BackupOffHostUnavailable || backup.IntegrityStatus != v1.IntegrityNotAvailable || backup.ArtifactSHA256 != nil || backup.SnapshotID != snapshot.ID || backup.NodeAffinityID == nil || *backup.NodeAffinityID != node.ID {
		t.Fatalf("backup API response claims off-host artifact or lost scope/affinity: %+v", backup)
	}
	pagedSnapshotBackups := collectPages[v1.BackupMetadata](t, server.Client(), server.URL, backupPath, "integration-operator-token")
	if len(pagedSnapshotBackups) != 1 || pagedSnapshotBackups[0].ID != backup.ID {
		t.Fatalf("snapshot backup pages=%+v", pagedSnapshotBackups)
	}
	pagedProjectBackups := collectPages[v1.BackupMetadata](t, server.Client(), server.URL,
		v1.BasePath+"/projects/"+url.PathEscape(projectID)+"/backups", "integration-operator-token")
	if len(pagedProjectBackups) != 1 || pagedProjectBackups[0].ID != backup.ID {
		t.Fatalf("project backup pages=%+v", pagedProjectBackups)
	}
	for _, unsupportedPath := range []string{v1.BasePath + "/volumes/" + url.PathEscape(volume.ID) + "/attach", v1.BasePath + "/backups/" + url.PathEscape(backup.ID) + "/restore"} {
		unsupported := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+unsupportedPath, "integration-operator-token", struct{}{}, nil)
		if unsupported.StatusCode != http.StatusNotFound {
			t.Fatalf("unsupported storage operation %s status=%d body=%s", unsupportedPath, unsupported.StatusCode, readBody(t, unsupported))
		}
	}

	before, err := repository.ListReleases(ctx, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedRequest := validReleaseRequest
	blockedRequest.IdempotencyKey = "http-api-blocked-release-once"
	blocked := doAPIJSON(t, server.Client(), http.MethodPost, server.URL+v1.BasePath+"/services/"+url.PathEscape(service.ID)+"/releases", "integration-operator-token", blockedRequest, nil)
	if blocked.StatusCode != http.StatusAccepted {
		t.Fatalf("source build gate status=%d body=%s", blocked.StatusCode, readBody(t, blocked))
	}
	var blockedResult struct {
		Release   v1.Release          `json:"release"`
		Operation v1.ReleaseOperation `json:"operation"`
	}
	if err := json.NewDecoder(blocked.Body).Decode(&blockedResult); err != nil {
		blocked.Body.Close()
		t.Fatalf("decode blocked release intent: %v", err)
	}
	blocked.Body.Close()
	if blockedResult.Release.State != v1.ReleaseBlocked || blockedResult.Release.Intent != v1.ReleaseIntentPreview || blockedResult.Release.ResolvedCommit != "" ||
		blockedResult.Operation.State != v1.ReleaseOperationBlocked || blockedResult.Operation.Blocker != "builder_unavailable" {
		t.Fatalf("blocked GitOps intent claimed build success: %+v", blockedResult)
	}
	after, err := repository.ListReleases(ctx, service.ID)
	foundIntent := false
	for _, recorded := range after {
		if recorded.ID == blockedResult.Release.ID {
			foundIntent = true
			break
		}
	}
	if err != nil || len(after) != len(before)+1 || !foundIntent {
		t.Fatalf("blocked Release intent was not durably recorded: before=%d after=%d result=%s err=%v", len(before), len(after), blockedResult.Release.ID, err)
	}

	events := collectPages[v1.Event](t, server.Client(), server.URL, v1.BasePath+"/events", "integration-operator-token")
	if len(events) == 0 {
		t.Fatal("durable resource event stream is empty after persisted mutations")
	}
	seenTypes := make(map[string]bool, len(events))
	for i, event := range events {
		seenTypes[event.Type] = true
		if i > 0 {
			previous := events[i-1]
			if event.OccurredAt.After(previous.OccurredAt) || (event.OccurredAt.Equal(previous.OccurredAt) && event.ID > previous.ID) {
				t.Fatalf("event pages are not descending: previous=%+v current=%+v", previous, event)
			}
		}
	}
	for _, eventType := range []string{"project.created", "service.created", "fleet_node.created", "release.created", "release.state_changed", "instance.created", "domain_claim.created", "domain_claim.verified", "gateway_route.created", "gateway_route.desired_state_changed", "volume.metadata_created", "snapshot.metadata_created", "backup.metadata_created"} {
		if !seenTypes[eventType] {
			t.Errorf("durable event stream is missing %q; saw %v", eventType, seenTypes)
		}
	}
	var sawClaimEvent, sawRouteEvent, sawVolumeEvent, sawSnapshotEvent, sawBackupEvent bool
	for _, event := range events {
		if event.Type == "domain_claim.verified" && event.DomainClaimID == claim.ID {
			sawClaimEvent = true
		}
		if event.Type == "gateway_route.desired_state_changed" && event.GatewayRouteID == route.ID && event.DomainClaimID == claim.ID {
			sawRouteEvent = true
		}
		if event.Type == "volume.metadata_created" && event.VolumeID == volume.ID && event.ProjectID == projectID && event.ServiceID == service.ID {
			sawVolumeEvent = true
		}
		if event.Type == "snapshot.metadata_created" && event.SnapshotID == snapshot.ID && event.InstanceID == createdInstances[0].ID {
			sawSnapshotEvent = true
		}
		if event.Type == "backup.metadata_created" && event.BackupID == backup.ID && event.SnapshotID == snapshot.ID {
			sawBackupEvent = true
		}
	}
	if !sawClaimEvent || !sawRouteEvent || !sawVolumeEvent || !sawSnapshotEvent || !sawBackupEvent {
		t.Errorf("events did not carry scoped resource references: claim=%v route=%v volume=%v snapshot=%v backup=%v", sawClaimEvent, sawRouteEvent, sawVolumeEvent, sawSnapshotEvent, sawBackupEvent)
	}
}

func doAPIJSON(t *testing.T, client *http.Client, method, target, token string, request, response any) *http.Response {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := doAPIRequest(client, method, target, token, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if response != nil && result.StatusCode >= 200 && result.StatusCode < 300 {
		if err := json.NewDecoder(result.Body).Decode(response); err != nil {
			result.Body.Close()
			t.Fatal(err)
		}
	}
	return result
}

func doAPIRequest(client *http.Client, method, target, token string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return client.Do(request)
}

func collectPages[T any](t *testing.T, client *http.Client, baseURL, path, token string) []T {
	t.Helper()
	cursor := ""
	var all []T
	for pages := 0; pages < 100; pages++ {
		target := baseURL + path + "?page_size=1"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		response, err := doAPIRequest(client, http.MethodGet, target, token, nil)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("list page %s status=%d body=%s", target, response.StatusCode, readBody(t, response))
		}
		var page v1.Page[T]
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all
		}
		cursor = page.NextCursor
	}
	t.Fatal("pagination exceeded 100 pages")
	return nil
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func assertIntegrationError(t *testing.T, response *http.Response, want v1.ErrorCode) {
	t.Helper()
	defer response.Body.Close()
	var envelope v1.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != want || envelope.Error.RequestID == "" || response.Header.Get("X-Request-ID") != envelope.Error.RequestID {
		t.Fatalf("error envelope=%+v header=%q want code=%q", envelope.Error, response.Header.Get("X-Request-ID"), want)
	}
}

func resetHTTPAPIIntegrationDB(ctx context.Context, db *sql.DB) error {
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

type integrationTXTResolver struct {
	records map[string][]string
}

func (r *integrationTXTResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return append([]string(nil), r.records[name]...), nil
}

func boolPointer(value bool) *bool { return &value }

func projectIDs(items []v1.Project) map[string]bool {
	ids := make(map[string]bool, len(items))
	for _, item := range items {
		ids[item.ID] = true
	}
	return ids
}

func serviceIDs(items []v1.Service) map[string]bool {
	ids := make(map[string]bool, len(items))
	for _, item := range items {
		ids[item.ID] = true
	}
	return ids
}

func instanceIDs(items []v1.Instance) map[string]bool {
	ids := make(map[string]bool, len(items))
	for _, item := range items {
		ids[item.ID] = true
	}
	return ids
}

func assertSameIDs(t *testing.T, want, got map[string]bool) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("page IDs count: got=%v want=%v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("page IDs missing %q: got=%v want=%v", id, got, want)
		}
	}
}
