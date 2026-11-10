package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "porter/controlplane/contract/v1"
	pgstore "porter/controlplane/internal/postgres/store"
)

const testAdminToken = "test-operator-token"

type stubRepository struct {
	project             v1.Project
	projects            []v1.Project
	service             v1.Service
	services            []v1.Service
	release             v1.Release
	releases            []v1.Release
	releaseOperations   []v1.ReleaseOperation
	blockedReleaseCalls int
	promotionCalls      int
	lastReleaseRequest  v1.CreateReleaseRequest
	lastIdempotencyKey  string
	instance            v1.Instance
	instances           []v1.Instance
	fleetNodes          []v1.FleetNode
	events              []v1.Event
	telemetryLogs       []v1.TelemetryLog
	telemetryMetrics    []v1.TelemetryMetric
	logFilter           pgstore.TelemetryFilter
	metricFilter        pgstore.TelemetryFilter
	logLimit            int
	metricLimit         int
	operation           v1.Operation
	operations          []v1.Operation
	domainClaim         v1.DomainClaim
	domainClaims        []v1.DomainClaim
	gatewayRoute        v1.GatewayRoute
	gatewayRoutes       []v1.GatewayRoute
	volume              v1.Volume
	volumes             []v1.Volume
	snapshot            v1.SnapshotMetadata
	snapshots           []v1.SnapshotMetadata
	backup              v1.BackupMetadata
	backups             []v1.BackupMetadata
	createProjectErr    error
	createServiceErr    error
	createClaimErr      error
	createVolumeErr     error
	createSnapshotErr   error
	queueSnapshotErr    error
	createBackupErr     error
	projectCalls        int
	serviceCalls        int
	claimCalls          int
	proofCalls          int
	volumeCalls         int
	snapshotCalls       int
	captureCalls        int
	restoreCalls        int
	backupCalls         int
}

func (s *stubRepository) CreateProject(_ context.Context, name string) (v1.Project, error) {
	s.projectCalls++
	if s.createProjectErr != nil {
		return v1.Project{}, s.createProjectErr
	}
	project := s.project
	project.Name = name
	return project, nil
}

func (s *stubRepository) GetProject(_ context.Context, id string) (v1.Project, error) {
	if s.project.ID == id {
		return s.project, nil
	}
	for _, project := range s.projects {
		if project.ID == id {
			return project, nil
		}
	}
	return v1.Project{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListProjectsPage(_ context.Context, limit int, after *pgstore.TimeCursor) ([]v1.Project, error) {
	return pageAfterTime(s.projects, limit, after, func(project v1.Project) (time.Time, string) {
		return project.CreatedAt, project.ID
	}), nil
}

func (s *stubRepository) CreateService(_ context.Context, _ string, _ v1.CreateServiceRequest) (v1.Service, error) {
	s.serviceCalls++
	if s.createServiceErr != nil {
		return v1.Service{}, s.createServiceErr
	}
	return s.service, nil
}

func (s *stubRepository) GetService(_ context.Context, id string) (v1.Service, error) {
	if s.service.ID == id {
		return s.service, nil
	}
	for _, service := range s.services {
		if service.ID == id {
			return service, nil
		}
	}
	return v1.Service{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListServicesPage(_ context.Context, _ string, limit int, after *pgstore.TimeCursor) ([]v1.Service, error) {
	return pageAfterTime(s.services, limit, after, func(service v1.Service) (time.Time, string) {
		return service.CreatedAt, service.ID
	}), nil
}

func (s *stubRepository) GetRelease(_ context.Context, id string) (v1.Release, error) {
	if s.release.ID == id {
		return s.release, nil
	}
	for _, release := range s.releases {
		if release.ID == id {
			return release, nil
		}
	}
	return v1.Release{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListReleasesPage(_ context.Context, _ string, limit int, after *pgstore.ReleaseCursor) ([]v1.Release, error) {
	items := make([]v1.Release, 0, len(s.releases))
	for _, release := range s.releases {
		if after == nil || release.Sequence < after.Sequence || (release.Sequence == after.Sequence && release.ID > after.ID) {
			items = append(items, release)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *stubRepository) CreateBlockedReleaseIntent(_ context.Context, serviceID string, request v1.CreateReleaseRequest, idempotencyKey string) (v1.Release, v1.ReleaseOperation, error) {
	s.blockedReleaseCalls++
	s.lastReleaseRequest = request
	s.lastIdempotencyKey = idempotencyKey
	release := s.release
	if release.ID == "" {
		release.ID = "rel_blocked"
	}
	release.ServiceID = serviceID
	release.State = v1.ReleaseBlocked
	release.Intent = request.Intent
	release.Source = request.Source
	release.Build = request.Build
	operation := v1.ReleaseOperation{ID: "rop_blocked", ServiceID: serviceID, ReleaseID: release.ID,
		Kind: v1.ReleaseOperationBuild, State: v1.ReleaseOperationBlocked, Intent: request.Intent,
		Blocker: "builder_unavailable", RequestedRef: request.Source.Ref}
	s.release = release
	s.releaseOperations = append(s.releaseOperations, operation)
	return release, operation, nil
}

func (s *stubRepository) ListReleaseOperations(_ context.Context, releaseID string, _ int) ([]v1.ReleaseOperation, error) {
	items := make([]v1.ReleaseOperation, 0, len(s.releaseOperations))
	for _, operation := range s.releaseOperations {
		if operation.ReleaseID == releaseID {
			items = append(items, operation)
		}
	}
	return items, nil
}

func (s *stubRepository) RequestProductionPromotion(_ context.Context, releaseID, idempotencyKey string) (v1.ReleaseOperation, error) {
	s.promotionCalls++
	s.lastIdempotencyKey = idempotencyKey
	operation := v1.ReleaseOperation{ID: "rop_promotion", ServiceID: "svc_1", ReleaseID: releaseID,
		Kind: v1.ReleaseOperationPromotion, State: v1.ReleaseOperationBlocked, Intent: v1.ReleaseIntentProduction,
		Blocker: "immutable_commit_pinned", Checks: []v1.ReleasePromotionCheck{{Name: "immutable_commit_pinned", Passed: false}}}
	s.releaseOperations = append(s.releaseOperations, operation)
	return operation, nil
}

func (s *stubRepository) GetInstance(_ context.Context, id string) (v1.Instance, error) {
	if s.instance.ID == id {
		return s.instance, nil
	}
	for _, instance := range s.instances {
		if instance.ID == id {
			return instance, nil
		}
	}
	return v1.Instance{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListInstancesPage(_ context.Context, _ string, limit int, after *pgstore.TimeCursor) ([]v1.Instance, error) {
	return pageAfterTime(s.instances, limit, after, func(instance v1.Instance) (time.Time, string) {
		return instance.CreatedAt, instance.ID
	}), nil
}

func (s *stubRepository) ListFleetNodesPage(_ context.Context, limit int, after *pgstore.TimeCursor) ([]v1.FleetNode, error) {
	return pageAfterTime(s.fleetNodes, limit, after, func(node v1.FleetNode) (time.Time, string) {
		return node.CreatedAt, node.ID
	}), nil
}

func (s *stubRepository) ListEventsPage(_ context.Context, limit int, after *pgstore.EventCursor) ([]v1.Event, error) {
	items := make([]v1.Event, 0, len(s.events))
	for _, event := range s.events {
		if after == nil || event.OccurredAt.Before(after.OccurredAt) || (event.OccurredAt.Equal(after.OccurredAt) && event.ID < after.ID) {
			items = append(items, event)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *stubRepository) ListTelemetryLogsPage(_ context.Context, limit int, filter pgstore.TelemetryFilter, _ *pgstore.EventCursor) ([]v1.TelemetryLog, error) {
	s.logFilter, s.logLimit = filter, limit
	items := append([]v1.TelemetryLog(nil), s.telemetryLogs...)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *stubRepository) ListTelemetryMetricsPage(_ context.Context, limit int, filter pgstore.TelemetryFilter, _ *pgstore.EventCursor) ([]v1.TelemetryMetric, error) {
	s.metricFilter, s.metricLimit = filter, limit
	items := append([]v1.TelemetryMetric(nil), s.telemetryMetrics...)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *stubRepository) GetOperation(_ context.Context, id string) (v1.Operation, error) {
	if s.operation.ID == id {
		return s.operation, nil
	}
	for _, operation := range s.operations {
		if operation.ID == id {
			return operation, nil
		}
	}
	return v1.Operation{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListOperationsPage(_ context.Context, limit int, after *pgstore.TimeCursor) ([]v1.Operation, error) {
	items := make([]v1.Operation, 0, len(s.operations)+1)
	if s.operation.ID != "" {
		items = append(items, s.operation)
	}
	items = append(items, s.operations...)
	filtered := items[:0]
	for _, operation := range items {
		if after == nil || operation.CreatedAt.Before(after.CreatedAt) || (operation.CreatedAt.Equal(after.CreatedAt) && operation.ID < after.ID) {
			filtered = append(filtered, operation)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *stubRepository) CreateDomainClaim(_ context.Context, projectID, serviceID, hostname string) (v1.DomainClaim, error) {
	s.claimCalls++
	if s.createClaimErr != nil {
		return v1.DomainClaim{}, s.createClaimErr
	}
	claim := s.domainClaim
	if claim.ID == "" {
		claim.ID = "dcl_created"
	}
	claim.ProjectID = projectID
	claim.ServiceID = serviceID
	claim.Hostname = hostname
	return claim, nil
}

func (s *stubRepository) GetDomainClaim(_ context.Context, id string) (v1.DomainClaim, error) {
	if s.domainClaim.ID == id {
		return s.domainClaim, nil
	}
	for _, claim := range s.domainClaims {
		if claim.ID == id {
			return claim, nil
		}
	}
	return v1.DomainClaim{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListDomainClaimsPage(_ context.Context, projectID, serviceID string, limit int, after *pgstore.TimeCursor) ([]v1.DomainClaim, error) {
	items := make([]v1.DomainClaim, 0, len(s.domainClaims)+1)
	if s.domainClaim.ID != "" {
		items = append(items, s.domainClaim)
	}
	items = append(items, s.domainClaims...)
	filtered := items[:0]
	for _, claim := range items {
		if projectID != "" && claim.ProjectID != projectID || serviceID != "" && claim.ServiceID != serviceID {
			continue
		}
		if after == nil || claim.CreatedAt.After(after.CreatedAt) || (claim.CreatedAt.Equal(after.CreatedAt) && claim.ID > after.ID) {
			filtered = append(filtered, claim)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *stubRepository) RecordDomainTXTProof(_ context.Context, id, observedValue string) (v1.DomainClaim, error) {
	s.proofCalls++
	claim, err := s.GetDomainClaim(context.Background(), id)
	if err != nil {
		return v1.DomainClaim{}, err
	}
	if observedValue != claim.VerificationTXTValue {
		return v1.DomainClaim{}, pgstore.ErrFailedPrecondition
	}
	claim.State = v1.DomainClaimVerified
	claim.OwnershipProofTXTValue = observedValue
	provedAt := time.Now().UTC()
	claim.OwnershipProvedAt = &provedAt
	s.domainClaim = claim
	return claim, nil
}

func (s *stubRepository) CreateGatewayRoute(_ context.Context, claimID, pathPrefix string) (v1.GatewayRoute, error) {
	claim, err := s.GetDomainClaim(context.Background(), claimID)
	if err != nil {
		return v1.GatewayRoute{}, err
	}
	route := s.gatewayRoute
	if route.ID == "" {
		route.ID = "grt_created"
	}
	route.DomainClaimID = claim.ID
	route.ProjectID = claim.ProjectID
	route.ServiceID = claim.ServiceID
	route.Hostname = claim.Hostname
	route.PathPrefix = pathPrefix
	route.GatewayStatus = v1.GatewayUnavailable
	s.gatewayRoute = route
	return route, nil
}

func (s *stubRepository) GetGatewayRoute(_ context.Context, id string) (v1.GatewayRoute, error) {
	if s.gatewayRoute.ID == id {
		return s.gatewayRoute, nil
	}
	for _, route := range s.gatewayRoutes {
		if route.ID == id {
			return route, nil
		}
	}
	return v1.GatewayRoute{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListGatewayRoutesPage(_ context.Context, claimID string, limit int, after *pgstore.TimeCursor) ([]v1.GatewayRoute, error) {
	items := make([]v1.GatewayRoute, 0, len(s.gatewayRoutes)+1)
	if s.gatewayRoute.ID != "" {
		items = append(items, s.gatewayRoute)
	}
	items = append(items, s.gatewayRoutes...)
	filtered := items[:0]
	for _, route := range items {
		if route.DomainClaimID != claimID || (after != nil && !(route.CreatedAt.After(after.CreatedAt) || (route.CreatedAt.Equal(after.CreatedAt) && route.ID > after.ID))) {
			continue
		}
		filtered = append(filtered, route)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *stubRepository) SetGatewayRouteDesiredEnabled(_ context.Context, id string, desiredEnabled bool) (v1.GatewayRoute, error) {
	route, err := s.GetGatewayRoute(context.Background(), id)
	if err != nil {
		return v1.GatewayRoute{}, err
	}
	claim, err := s.GetDomainClaim(context.Background(), route.DomainClaimID)
	if err != nil {
		return v1.GatewayRoute{}, err
	}
	if desiredEnabled && claim.State != v1.DomainClaimVerified {
		return v1.GatewayRoute{}, pgstore.ErrFailedPrecondition
	}
	route.DesiredEnabled = desiredEnabled
	route.GatewayStatus = v1.GatewayUnavailable
	s.gatewayRoute = route
	return route, nil
}

func (s *stubRepository) CreateVolume(_ context.Context, projectID, serviceID string, request v1.CreateVolumeRequest) (v1.Volume, error) {
	s.volumeCalls++
	if s.createVolumeErr != nil {
		return v1.Volume{}, s.createVolumeErr
	}
	volume := s.volume
	if volume.ID == "" {
		volume.ID = "vol_created"
	}
	volume.ProjectID = projectID
	volume.ServiceID = serviceID
	volume.Name = request.Name
	volume.SizeMiB = request.SizeMiB
	volume.MountPath = request.MountPath
	volume.AccessMode = v1.VolumeSingleWriter
	volume.State = v1.VolumeMetadataOnly
	volume.IntegrityStatus = v1.IntegrityNotAvailable
	if request.NodeAffinityID != "" {
		volume.NodeAffinityID = &request.NodeAffinityID
	}
	s.volume = volume
	return volume, nil
}

func (s *stubRepository) GetVolume(_ context.Context, id string) (v1.Volume, error) {
	if s.volume.ID == id {
		return s.volume, nil
	}
	for _, volume := range s.volumes {
		if volume.ID == id {
			return volume, nil
		}
	}
	return v1.Volume{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListVolumesPage(_ context.Context, projectID, serviceID string, limit int, after *pgstore.TimeCursor) ([]v1.Volume, error) {
	items := make([]v1.Volume, 0, len(s.volumes)+1)
	if s.volume.ID != "" {
		items = append(items, s.volume)
	}
	items = append(items, s.volumes...)
	filtered := items[:0]
	for _, volume := range items {
		if volume.ProjectID != projectID || (serviceID != "" && volume.ServiceID != serviceID) ||
			(after != nil && !(volume.CreatedAt.After(after.CreatedAt) || (volume.CreatedAt.Equal(after.CreatedAt) && volume.ID > after.ID))) {
			continue
		}
		filtered = append(filtered, volume)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *stubRepository) ListProjectInstancesPage(_ context.Context, _ string, limit int, after *pgstore.TimeCursor) ([]v1.Instance, error) {
	return pageAfterTime(s.instances, limit, after, func(instance v1.Instance) (time.Time, string) {
		return instance.CreatedAt, instance.ID
	}), nil
}

func (s *stubRepository) CreateSnapshotMetadata(_ context.Context, instanceID, consistency string) (v1.SnapshotMetadata, error) {
	s.snapshotCalls++
	if s.createSnapshotErr != nil {
		return v1.SnapshotMetadata{}, s.createSnapshotErr
	}
	instance, err := s.GetInstance(context.Background(), instanceID)
	if err != nil {
		return v1.SnapshotMetadata{}, err
	}
	service, err := s.GetService(context.Background(), instance.ServiceID)
	if err != nil {
		return v1.SnapshotMetadata{}, err
	}
	snapshot := s.snapshot
	if snapshot.ID == "" {
		snapshot.ID = "snp_created"
	}
	snapshot.ProjectID = service.ProjectID
	snapshot.ServiceID = instance.ServiceID
	snapshot.InstanceID = instanceID
	snapshot.Consistency = consistency
	snapshot.CaptureStatus = v1.SnapshotCaptureUnavailable
	snapshot.IntegrityStatus = v1.IntegrityNotAvailable
	if instance.NodeID != "" {
		snapshot.NodeAffinityID = &instance.NodeID
	}
	s.snapshot = snapshot
	return snapshot, nil
}

func (s *stubRepository) QueueSnapshotCreateOperation(_ context.Context, instanceID, consistency, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error) {
	s.captureCalls++
	if s.queueSnapshotErr != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, s.queueSnapshotErr
	}
	snapshot, err := s.CreateSnapshotMetadata(context.Background(), instanceID, consistency)
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	snapshot.CaptureStatus = "queued"
	snapshot.RestoreStatus = "not_requested"
	s.snapshot = snapshot
	operation := s.operation
	if operation.ID == "" {
		operation = v1.Operation{ID: "op_capture", State: v1.OperationQueued}
	}
	return snapshot, operation, nil
}

func (s *stubRepository) QueueSnapshotRestoreOperation(_ context.Context, instanceID, snapshotID, idempotencyKey string) (v1.SnapshotMetadata, v1.Operation, error) {
	s.restoreCalls++
	if s.queueSnapshotErr != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, s.queueSnapshotErr
	}
	snapshot, err := s.GetSnapshotMetadata(context.Background(), snapshotID)
	if err != nil {
		return v1.SnapshotMetadata{}, v1.Operation{}, err
	}
	snapshot.RestoreStatus = "queued"
	s.snapshot = snapshot
	operation := s.operation
	if operation.ID == "" {
		operation = v1.Operation{ID: "op_restore", State: v1.OperationQueued}
	}
	return snapshot, operation, nil
}

func (s *stubRepository) GetSnapshotMetadata(_ context.Context, id string) (v1.SnapshotMetadata, error) {
	if s.snapshot.ID == id {
		return s.snapshot, nil
	}
	for _, snapshot := range s.snapshots {
		if snapshot.ID == id {
			return snapshot, nil
		}
	}
	return v1.SnapshotMetadata{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListSnapshotMetadataPage(_ context.Context, projectID, instanceID string, limit int, after *pgstore.TimeCursor) ([]v1.SnapshotMetadata, error) {
	items := make([]v1.SnapshotMetadata, 0, len(s.snapshots)+1)
	if s.snapshot.ID != "" {
		items = append(items, s.snapshot)
	}
	items = append(items, s.snapshots...)
	filtered := items[:0]
	for _, snapshot := range items {
		if snapshot.ProjectID != projectID || (instanceID != "" && snapshot.InstanceID != instanceID) ||
			(after != nil && !(snapshot.CreatedAt.After(after.CreatedAt) || (snapshot.CreatedAt.Equal(after.CreatedAt) && snapshot.ID > after.ID))) {
			continue
		}
		filtered = append(filtered, snapshot)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *stubRepository) CreateBackupMetadata(_ context.Context, snapshotID, consistency string) (v1.BackupMetadata, error) {
	s.backupCalls++
	if s.createBackupErr != nil {
		return v1.BackupMetadata{}, s.createBackupErr
	}
	snapshot, err := s.GetSnapshotMetadata(context.Background(), snapshotID)
	if err != nil {
		return v1.BackupMetadata{}, err
	}
	backup := s.backup
	if backup.ID == "" {
		backup.ID = "bkp_created"
	}
	backup.ProjectID = snapshot.ProjectID
	backup.ServiceID = snapshot.ServiceID
	backup.InstanceID = snapshot.InstanceID
	backup.SnapshotID = snapshotID
	backup.Consistency = consistency
	backup.State = v1.BackupMetadataOnly
	backup.OffHostStatus = v1.BackupOffHostUnavailable
	backup.NodeAffinityID = snapshot.NodeAffinityID
	backup.IntegrityStatus = v1.IntegrityNotAvailable
	s.backup = backup
	return backup, nil
}

func (s *stubRepository) GetBackupMetadata(_ context.Context, id string) (v1.BackupMetadata, error) {
	if s.backup.ID == id {
		return s.backup, nil
	}
	for _, backup := range s.backups {
		if backup.ID == id {
			return backup, nil
		}
	}
	return v1.BackupMetadata{}, pgstore.ErrNotFound
}

func (s *stubRepository) ListBackupMetadataPage(_ context.Context, projectID, snapshotID string, limit int, after *pgstore.TimeCursor) ([]v1.BackupMetadata, error) {
	items := make([]v1.BackupMetadata, 0, len(s.backups)+1)
	if s.backup.ID != "" {
		items = append(items, s.backup)
	}
	items = append(items, s.backups...)
	filtered := items[:0]
	for _, backup := range items {
		if backup.ProjectID != projectID || (snapshotID != "" && backup.SnapshotID != snapshotID) ||
			(after != nil && !(backup.CreatedAt.After(after.CreatedAt) || (backup.CreatedAt.Equal(after.CreatedAt) && backup.ID > after.ID))) {
			continue
		}
		filtered = append(filtered, backup)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func pageAfterTime[T any](items []T, limit int, after *pgstore.TimeCursor, key func(T) (time.Time, string)) []T {
	page := make([]T, 0, len(items))
	for _, item := range items {
		createdAt, id := key(item)
		if after == nil || createdAt.After(after.CreatedAt) || (createdAt.Equal(after.CreatedAt) && id > after.ID) {
			page = append(page, item)
		}
	}
	if len(page) > limit {
		page = page[:limit]
	}
	return page
}

func TestNewRequiresRepositoryAndOperatorToken(t *testing.T) {
	if _, err := New(nil, testAdminToken); err == nil {
		t.Fatal("New accepted a nil repository")
	}
	if _, err := New(&stubRepository{}, ""); err == nil {
		t.Fatal("New accepted an empty admin token")
	}
}

func TestBearerAuthenticationAndStableUnauthorizedEnvelope(t *testing.T) {
	server := mustServer(t, &stubRepository{})
	for _, authorization := range []string{"", "Basic " + testAdminToken, "Bearer wrong", "Bearer " + testAdminToken + " extra"} {
		recorder := serve(server, http.MethodGet, "/api/v1/projects", authorization, "", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, recorder.Code)
		}
		assertErrorEnvelope(t, recorder, v1.ErrorUnauthenticated)
	}
	recorder := serve(server, http.MethodGet, "/api/v1/projects", "Bearer "+testAdminToken, "", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid bearer status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProjectRequestValidationAndLocation(t *testing.T) {
	project := v1.Project{ID: "prj_123", State: v1.ProjectActive}
	repo := &stubRepository{project: project}
	server := mustServer(t, repo)

	for _, body := range []string{`{"name":"   "}`, `{"name":"ok","unexpected":true}`, `{"name":"ok"} {}`} {
		recorder := serve(server, http.MethodPost, "/api/v1/projects", "Bearer "+testAdminToken, "application/json", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s status = %d, body=%s", body, recorder.Code, recorder.Body.String())
		}
		assertErrorEnvelope(t, recorder, v1.ErrorBadRequest)
	}
	if repo.projectCalls != 0 {
		t.Fatalf("invalid requests reached repository %d times", repo.projectCalls)
	}

	recorder := serve(server, http.MethodPost, "/api/v1/projects", "Bearer "+testAdminToken, "application/json", `{"name":" demo "}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "/api/v1/projects/prj_123" {
		t.Fatalf("Location = %q", got)
	}
	var created v1.Project
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.Name != "demo" {
		t.Fatalf("created project = %+v, err=%v", created, err)
	}
}

func TestServiceRequestValidationRejectsNonFirecrackerAndOutOfRangePort(t *testing.T) {
	repo := &stubRepository{}
	server := mustServer(t, repo)
	for _, body := range []string{
		`{"name":"web","runtime":"docker","port":8080}`,
		`{"name":"web","runtime":"firecracker","port":65536}`,
		`{"name":"web","runtime":"firecracker","port":8080,"unexpected":true}`,
	} {
		recorder := serve(server, http.MethodPost, "/api/v1/projects/prj_123/services", "Bearer "+testAdminToken, "application/json", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid service body %s status=%d body=%s", body, recorder.Code, recorder.Body.String())
		}
		assertErrorEnvelope(t, recorder, v1.ErrorBadRequest)
	}
	if repo.serviceCalls != 0 {
		t.Fatalf("invalid Service requests reached repository %d times", repo.serviceCalls)
	}
}

func TestProjectKeysetPaginationAndCursorScope(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &stubRepository{projects: []v1.Project{
		{ID: "prj_1", Name: "one", CreatedAt: base},
		{ID: "prj_2", Name: "two", CreatedAt: base.Add(time.Second)},
	}}
	server := mustServer(t, repo)
	first := serve(server, http.MethodGet, "/api/v1/projects?page_size=1", "Bearer "+testAdminToken, "", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body=%s", first.Code, first.Body.String())
	}
	var firstPage v1.Page[v1.Project]
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != "prj_1" || firstPage.NextCursor == "" {
		t.Fatalf("first page = %+v", firstPage)
	}
	second := serve(server, http.MethodGet, "/api/v1/projects?page_size=1&cursor="+firstPage.NextCursor, "Bearer "+testAdminToken, "", "")
	if second.Code != http.StatusOK {
		t.Fatalf("second page status = %d, body=%s", second.Code, second.Body.String())
	}
	var secondPage v1.Page[v1.Project]
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != "prj_2" || secondPage.NextCursor != "" {
		t.Fatalf("second page = %+v", secondPage)
	}

	wrongScope := serve(server, http.MethodGet, "/api/v1/projects/prj_1/services?page_size=1&cursor="+firstPage.NextCursor, "Bearer "+testAdminToken, "", "")
	if wrongScope.Code != http.StatusBadRequest {
		t.Fatalf("cross-resource cursor status = %d, body=%s", wrongScope.Code, wrongScope.Body.String())
	}
	assertErrorEnvelope(t, wrongScope, v1.ErrorBadRequest)

	invalidSize := serve(server, http.MethodGet, "/api/v1/projects?page_size=101", "Bearer "+testAdminToken, "", "")
	if invalidSize.Code != http.StatusBadRequest {
		t.Fatalf("invalid page size status = %d", invalidSize.Code)
	}
	assertErrorEnvelope(t, invalidSize, v1.ErrorBadRequest)
}

func TestFleetNodeAndEventPaginationIsReadOnlyAndCursorScoped(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	repo := &stubRepository{
		fleetNodes: []v1.FleetNode{
			{ID: "node_a", Name: "one", State: v1.FleetNodeOffline, CreatedAt: base},
			{ID: "node_b", Name: "two", State: v1.FleetNodeReady, CreatedAt: base.Add(time.Second)},
		},
		events: []v1.Event{
			{ID: "evt_b", Type: "operation.succeeded", OccurredAt: base.Add(time.Second), FleetNodeID: "node_b", OperationID: "op_1", CommandID: "cmd_1"},
			{ID: "evt_a", Type: "project.created", OccurredAt: base, ProjectID: "prj_a"},
		},
	}
	server := mustServer(t, repo)

	nodeFirst := serve(server, http.MethodGet, "/api/v1/fleet/nodes?page_size=1", "Bearer "+testAdminToken, "", "")
	if nodeFirst.Code != http.StatusOK {
		t.Fatalf("fleet node first page status=%d body=%s", nodeFirst.Code, nodeFirst.Body.String())
	}
	var nodePage v1.Page[v1.FleetNode]
	if err := json.Unmarshal(nodeFirst.Body.Bytes(), &nodePage); err != nil {
		t.Fatal(err)
	}
	if len(nodePage.Items) != 1 || nodePage.Items[0].ID != "node_a" || nodePage.NextCursor == "" {
		t.Fatalf("fleet node first page = %+v", nodePage)
	}
	nodeSecond := serve(server, http.MethodGet, "/api/v1/fleet/nodes?page_size=1&cursor="+nodePage.NextCursor, "Bearer "+testAdminToken, "", "")
	var nodePage2 v1.Page[v1.FleetNode]
	if nodeSecond.Code != http.StatusOK || json.Unmarshal(nodeSecond.Body.Bytes(), &nodePage2) != nil || len(nodePage2.Items) != 1 || nodePage2.Items[0].ID != "node_b" || nodePage2.NextCursor != "" {
		t.Fatalf("fleet node second page status=%d page=%+v body=%s", nodeSecond.Code, nodePage2, nodeSecond.Body.String())
	}

	eventFirst := serve(server, http.MethodGet, "/api/v1/events?page_size=1", "Bearer "+testAdminToken, "", "")
	if eventFirst.Code != http.StatusOK {
		t.Fatalf("event first page status=%d body=%s", eventFirst.Code, eventFirst.Body.String())
	}
	var eventPage v1.Page[v1.Event]
	if err := json.Unmarshal(eventFirst.Body.Bytes(), &eventPage); err != nil {
		t.Fatal(err)
	}
	if len(eventPage.Items) != 1 || eventPage.Items[0].ID != "evt_b" || eventPage.Items[0].FleetNodeID != "node_b" ||
		eventPage.Items[0].OperationID != "op_1" || eventPage.Items[0].CommandID != "cmd_1" || eventPage.NextCursor == "" {
		t.Fatalf("event first page = %+v", eventPage)
	}
	eventSecond := serve(server, http.MethodGet, "/api/v1/events?page_size=1&cursor="+eventPage.NextCursor, "Bearer "+testAdminToken, "", "")
	var eventPage2 v1.Page[v1.Event]
	if eventSecond.Code != http.StatusOK || json.Unmarshal(eventSecond.Body.Bytes(), &eventPage2) != nil || len(eventPage2.Items) != 1 || eventPage2.Items[0].ID != "evt_a" || eventPage2.NextCursor != "" {
		t.Fatalf("event second page status=%d page=%+v body=%s", eventSecond.Code, eventPage2, eventSecond.Body.String())
	}
	crossCollection := serve(server, http.MethodGet, "/api/v1/fleet/nodes?page_size=1&cursor="+eventPage.NextCursor, "Bearer "+testAdminToken, "", "")
	if crossCollection.Code != http.StatusBadRequest {
		t.Fatalf("cross-collection cursor status=%d body=%s", crossCollection.Code, crossCollection.Body.String())
	}
	assertErrorEnvelope(t, crossCollection, v1.ErrorBadRequest)

	for _, path := range []string{"/api/v1/fleet/nodes", "/api/v1/events"} {
		writeAttempt := serve(server, http.MethodPost, path, "Bearer "+testAdminToken, "application/json", `{}`)
		if writeAttempt.Code != http.StatusMethodNotAllowed || writeAttempt.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("write attempt %s response=%d Allow=%q body=%s", path, writeAttempt.Code, writeAttempt.Header().Get("Allow"), writeAttempt.Body.String())
		}
		assertErrorEnvelope(t, writeAttempt, v1.ErrorBadRequest)
	}
	unauthorized := serve(server, http.MethodGet, "/api/v1/events", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated events status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestOperationsAreReadOnlyAndAgentActionsFailClosed(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	leaseExpiry := base.Add(time.Minute)
	newest := v1.Operation{
		ID: "op_new", ProjectID: "prj_1", ServiceID: "svc_1", InstanceID: "ins_1", ResourceID: "ins_1",
		Kind: v1.AgentCommandVMStop, State: v1.OperationRunning, CommandID: "cmd_1",
		CommandState: v1.AgentCommandLeased, Attempt: 2, LeaseExpiresAt: &leaseExpiry, CreatedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second),
	}
	older := v1.Operation{
		ID: "op_old", ProjectID: "prj_1", ServiceID: "svc_1", InstanceID: "ins_2", ResourceID: "ins_2",
		Kind: v1.AgentCommandVMStart, State: v1.OperationQueued, CommandID: "cmd_2",
		CommandState: v1.AgentCommandQueued, CreatedAt: base, UpdatedAt: base,
	}
	repo := &stubRepository{operation: newest, operations: []v1.Operation{older}}
	server := mustServer(t, repo)

	unauthorized := serve(server, http.MethodGet, "/api/v1/operations", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated operation list status=%d", unauthorized.Code)
	}

	first := serve(server, http.MethodGet, "/api/v1/operations?page_size=1", "Bearer "+testAdminToken, "", "")
	var firstPage v1.Page[v1.Operation]
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || len(firstPage.Items) != 1 || firstPage.Items[0].ID != newest.ID || firstPage.NextCursor == "" {
		t.Fatalf("operation first page status=%d page=%+v body=%s", first.Code, firstPage, first.Body.String())
	}
	second := serve(server, http.MethodGet, "/api/v1/operations?page_size=1&cursor="+firstPage.NextCursor, "Bearer "+testAdminToken, "", "")
	var secondPage v1.Page[v1.Operation]
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID != older.ID || secondPage.NextCursor != "" {
		t.Fatalf("operation second page status=%d page=%+v body=%s", second.Code, secondPage, second.Body.String())
	}

	detail := serve(server, http.MethodGet, "/api/v1/operations/op_new", "Bearer "+testAdminToken, "", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"command_state":"leased"`) || strings.Contains(detail.Body.String(), "lease_token") || strings.Contains(detail.Body.String(), `"payload"`) {
		t.Fatalf("operation detail exposed unsafe fields or omitted state: %d %s", detail.Code, detail.Body.String())
	}
	create := serve(server, http.MethodPost, "/api/v1/operations", "Bearer "+testAdminToken, "application/json", `{}`)
	if create.Code != http.StatusMethodNotAllowed || create.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("operation create status=%d Allow=%q", create.Code, create.Header().Get("Allow"))
	}
	retry := serve(server, http.MethodPost, "/api/v1/operations/op_new/retry", "Bearer "+testAdminToken, "application/json", `{}`)
	if retry.Code != http.StatusPreconditionFailed || !strings.Contains(retry.Body.String(), "unavailable") {
		t.Fatalf("retry should be explicitly unavailable: status=%d body=%s", retry.Code, retry.Body.String())
	}
	assertErrorEnvelope(t, retry, v1.ErrorFailedPrecondition)
	noAgentRoute := serve(server, http.MethodGet, "/api/v1/agent/commands", "Bearer "+testAdminToken, "", "")
	if noAgentRoute.Code != http.StatusNotFound {
		t.Fatalf("agent command route must not be mounted: status=%d body=%s", noAgentRoute.Code, noAgentRoute.Body.String())
	}
}

func TestDomainClaimsRequireDNSProofBeforeRouteIntentCanBeEnabled(t *testing.T) {
	claim := v1.DomainClaim{
		ID: "dcl_1", ProjectID: "prj_1", ServiceID: "svc_1", Hostname: "api.example.com",
		State: v1.DomainClaimPendingProof, VerificationTXTName: "_porter-verify.api.example.com",
		VerificationTXTValue: "porter-verify=" + strings.Repeat("a", 64),
	}
	route := v1.GatewayRoute{
		ID: "grt_1", DomainClaimID: claim.ID, ProjectID: claim.ProjectID, ServiceID: claim.ServiceID,
		Hostname: claim.Hostname, PathPrefix: "/", GatewayStatus: v1.GatewayUnavailable,
	}
	repo := &stubRepository{
		service:     v1.Service{ID: claim.ServiceID, ProjectID: claim.ProjectID, State: v1.ServiceActive},
		domainClaim: claim, gatewayRoute: route,
	}
	var resolvedName string
	resolver := txtResolverFunc(func(_ context.Context, name string) ([]string, error) {
		resolvedName = name
		return []string{claim.VerificationTXTValue}, nil
	})
	server, err := NewWithTXTResolver(repo, testAdminToken, resolver)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := serve(server, http.MethodGet, "/api/v1/domain-claims", "", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated domain read status=%d", unauthorized.Code)
	}

	wrongScope := serve(server, http.MethodPost, "/api/v1/projects/prj_other/services/svc_1/domain-claims", "Bearer "+testAdminToken, "application/json", `{"hostname":"api.example.com"}`)
	if wrongScope.Code != http.StatusNotFound || repo.claimCalls != 0 {
		t.Fatalf("cross-project claim create status=%d repositoryCalls=%d body=%s", wrongScope.Code, repo.claimCalls, wrongScope.Body.String())
	}
	wrongScopeList := serve(server, http.MethodGet, "/api/v1/projects/prj_other/services/svc_1/domain-claims", "Bearer "+testAdminToken, "", "")
	if wrongScopeList.Code != http.StatusNotFound {
		t.Fatalf("cross-project claim list status=%d body=%s", wrongScopeList.Code, wrongScopeList.Body.String())
	}

	tooEarly := serve(server, http.MethodPatch, "/api/v1/gateway-routes/grt_1", "Bearer "+testAdminToken, "application/json", `{"desired_enabled":true}`)
	if tooEarly.Code != http.StatusPreconditionFailed || repo.gatewayRoute.DesiredEnabled {
		t.Fatalf("route enable before TXT proof status=%d desired=%v body=%s", tooEarly.Code, repo.gatewayRoute.DesiredEnabled, tooEarly.Body.String())
	}
	assertErrorEnvelope(t, tooEarly, v1.ErrorFailedPrecondition)

	missingResolver := txtResolverFunc(func(context.Context, string) ([]string, error) { return nil, nil })
	missingServer, err := NewWithTXTResolver(repo, testAdminToken, missingResolver)
	if err != nil {
		t.Fatal(err)
	}
	missingProof := serve(missingServer, http.MethodPost, "/api/v1/domain-claims/dcl_1/verify-txt", "Bearer "+testAdminToken, "application/json", `{}`)
	if missingProof.Code != http.StatusPreconditionFailed || repo.proofCalls != 0 || repo.domainClaim.State != v1.DomainClaimPendingProof {
		t.Fatalf("missing TXT proof status=%d proofCalls=%d state=%q body=%s", missingProof.Code, repo.proofCalls, repo.domainClaim.State, missingProof.Body.String())
	}

	verified := serve(server, http.MethodPost, "/api/v1/domain-claims/dcl_1/verify-txt", "Bearer "+testAdminToken, "application/json", `{}`)
	if verified.Code != http.StatusOK || resolvedName != claim.VerificationTXTName+"." {
		t.Fatalf("verify TXT status=%d queried=%q body=%s", verified.Code, resolvedName, verified.Body.String())
	}
	var verifiedClaim v1.DomainClaim
	if err := json.Unmarshal(verified.Body.Bytes(), &verifiedClaim); err != nil {
		t.Fatal(err)
	}
	if verifiedClaim.State != v1.DomainClaimVerified || verifiedClaim.OwnershipProofTXTValue != claim.VerificationTXTValue || verifiedClaim.OwnershipProvedAt == nil {
		t.Fatalf("verified claim did not return persisted TXT proof: %+v", verifiedClaim)
	}

	enabled := serve(server, http.MethodPatch, "/api/v1/gateway-routes/grt_1", "Bearer "+testAdminToken, "application/json", `{"desired_enabled":true}`)
	if enabled.Code != http.StatusOK {
		t.Fatalf("route intent enable after proof status=%d body=%s", enabled.Code, enabled.Body.String())
	}
	var enabledRoute v1.GatewayRoute
	if err := json.Unmarshal(enabled.Body.Bytes(), &enabledRoute); err != nil {
		t.Fatal(err)
	}
	if !enabledRoute.DesiredEnabled || enabledRoute.GatewayStatus != v1.GatewayUnavailable {
		t.Fatalf("route response claims live routing or is not enabled as intent: %+v", enabledRoute)
	}
	invalidPatch := serve(server, http.MethodPatch, "/api/v1/gateway-routes/grt_1", "Bearer "+testAdminToken, "application/json", `{}`)
	if invalidPatch.Code != http.StatusBadRequest {
		t.Fatalf("missing desired_enabled status=%d body=%s", invalidPatch.Code, invalidPatch.Body.String())
	}
}

type txtResolverFunc func(context.Context, string) ([]string, error)

func (f txtResolverFunc) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return f(ctx, name)
}

func TestReleaseCreateFailsClosedUntilBuilderIsAvailable(t *testing.T) {
	repo := &stubRepository{service: v1.Service{ID: "svc_1", State: v1.ServiceActive}}
	server := mustServer(t, repo)
	body := `{"source":{"kind":"git","repository":"https://example.test/acme/repo.git","ref":"main"},"build":{"kind":"dockerfile","dockerfile_path":"Dockerfile"},"idempotency_key":"release-preview-1"}`
	recorder := serve(server, http.MethodPost, "/api/v1/services/svc_1/releases", "Bearer "+testAdminToken, "application/json", body)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("release create status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Release   v1.Release          `json:"release"`
		Operation v1.ReleaseOperation `json:"operation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode blocked release response: %v; body=%s", err, recorder.Body.String())
	}
	if result.Release.State != v1.ReleaseBlocked || result.Release.Intent != v1.ReleaseIntentPreview || result.Release.ResolvedCommit != "" || result.Operation.State != v1.ReleaseOperationBlocked || result.Operation.Blocker != "builder_unavailable" {
		t.Fatalf("release response claims unearned progress: %+v", result)
	}
	if repo.serviceCalls != 0 {
		t.Fatalf("unexpected service creation calls: %d", repo.serviceCalls)
	}
	if repo.blockedReleaseCalls != 1 || repo.lastIdempotencyKey != "release-preview-1" || repo.lastReleaseRequest.Intent != v1.ReleaseIntentPreview {
		t.Fatalf("blocked release intent was not persisted with default preview intent: calls=%d key=%q request=%+v", repo.blockedReleaseCalls, repo.lastIdempotencyKey, repo.lastReleaseRequest)
	}
}

func TestProductionPromotionPersistsChecksButNeverActivates(t *testing.T) {
	repo := &stubRepository{release: v1.Release{ID: "rel_1", ServiceID: "svc_1"}}
	server := mustServer(t, repo)
	recorder := serve(server, http.MethodPost, "/api/v1/releases/rel_1/promote", "Bearer "+testAdminToken, "application/json", `{"idempotency_key":"promote-rel-1"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("promotion request status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Operation v1.ReleaseOperation `json:"operation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode blocked promotion response: %v; body=%s", err, recorder.Body.String())
	}
	if result.Operation.State != v1.ReleaseOperationBlocked || result.Operation.Kind != v1.ReleaseOperationPromotion || result.Operation.Blocker != "immutable_commit_pinned" || len(result.Operation.Checks) == 0 {
		t.Fatalf("promotion response is not a persisted blocked check result: %+v", result.Operation)
	}
	if repo.promotionCalls != 1 || repo.lastIdempotencyKey != "promote-rel-1" {
		t.Fatalf("promotion was not recorded: calls=%d key=%q", repo.promotionCalls, repo.lastIdempotencyKey)
	}
	page := serve(server, http.MethodGet, "/api/v1/releases/rel_1/operations", "Bearer "+testAdminToken, "", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"production_promotion"`) || !strings.Contains(page.Body.String(), `"blocked"`) {
		t.Fatalf("release operation history omitted promotion status: %d %s", page.Code, page.Body.String())
	}
}

func TestExplicitProductionReleaseIntentIsRecordedButNotDeployed(t *testing.T) {
	repo := &stubRepository{service: v1.Service{ID: "svc_1", State: v1.ServiceActive}}
	server := mustServer(t, repo)
	body := `{"source":{"kind":"git","repository":"https://example.test/acme/repo.git","ref":"v1.2.3"},"build":{"kind":"dockerfile","dockerfile_path":"Dockerfile"},"intent":"production","idempotency_key":"release-prod-1"}`
	recorder := serve(server, http.MethodPost, "/api/v1/services/svc_1/releases", "Bearer "+testAdminToken, "application/json", body)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("production intent response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Release   v1.Release          `json:"release"`
		Operation v1.ReleaseOperation `json:"operation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode production intent response: %v; body=%s", err, recorder.Body.String())
	}
	if result.Release.Intent != v1.ReleaseIntentProduction || result.Operation.Intent != v1.ReleaseIntentProduction || result.Release.State != v1.ReleaseBlocked || result.Release.ResolvedCommit != "" {
		t.Fatalf("production intent was not distinct from build/deploy status: %+v", result)
	}
	if repo.lastReleaseRequest.Source.Ref != "v1.2.3" || repo.lastIdempotencyKey != "release-prod-1" {
		t.Fatalf("request/ref or idempotency key was not persisted: request=%+v key=%q", repo.lastReleaseRequest, repo.lastIdempotencyKey)
	}
}

func TestBlockedReleaseIntentRequiresIdempotencyKey(t *testing.T) {
	repo := &stubRepository{service: v1.Service{ID: "svc_1", State: v1.ServiceActive}}
	server := mustServer(t, repo)
	body := `{"source":{"kind":"git","repository":"https://example.test/acme/repo.git","ref":"main"},"build":{"kind":"dockerfile","dockerfile_path":"Dockerfile"}}`
	recorder := serve(server, http.MethodPost, "/api/v1/services/svc_1/releases", "Bearer "+testAdminToken, "application/json", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertErrorEnvelope(t, recorder, v1.ErrorBadRequest)
	if repo.blockedReleaseCalls != 0 {
		t.Fatalf("release intent without an idempotency key was persisted: calls=%d", repo.blockedReleaseCalls)
	}
}

func TestReleaseCreateRejectsTraversalBeforeBuilderOrPersistence(t *testing.T) {
	repo := &stubRepository{service: v1.Service{ID: "svc_1", State: v1.ServiceActive}}
	server := mustServer(t, repo)
	body := `{"source":{"kind":"git","repository":"https://example.test/acme/repo.git","ref":"main"},"build":{"kind":"dockerfile","dockerfile_path":"../../etc/passwd"}}`
	recorder := serve(server, http.MethodPost, "/api/v1/services/svc_1/releases", "Bearer "+testAdminToken, "application/json", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe release create status=%d body=%s; want 400", recorder.Code, recorder.Body.String())
	}
	assertErrorEnvelope(t, recorder, v1.ErrorBadRequest)
	if repo.serviceCalls != 0 {
		t.Fatalf("unsafe release request reached mutation path: service calls=%d", repo.serviceCalls)
	}
}

func TestRepositoryErrorsUseStablePublicMessages(t *testing.T) {
	repo := &stubRepository{createProjectErr: fakeSQLStateError{state: "23505", message: "duplicate key; private_sql"}}
	server := mustServer(t, repo)
	recorder := serve(server, http.MethodPost, "/api/v1/projects", "Bearer "+testAdminToken, "application/json", `{"name":"demo"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	assertErrorEnvelope(t, recorder, v1.ErrorConflict)
	if strings.Contains(recorder.Body.String(), "private_sql") {
		t.Fatalf("database detail leaked: %s", recorder.Body.String())
	}
}

func TestWrongMethodAndUnknownRouteUseErrorEnvelope(t *testing.T) {
	server := mustServer(t, &stubRepository{})
	method := serve(server, http.MethodDelete, "/api/v1/projects", "Bearer "+testAdminToken, "", "")
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("method response = %d Allow=%q", method.Code, method.Header().Get("Allow"))
	}
	assertErrorEnvelope(t, method, v1.ErrorBadRequest)
	missing := serve(server, http.MethodGet, "/api/v1/unimplemented", "Bearer "+testAdminToken, "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unimplemented route status = %d", missing.Code)
	}
	assertErrorEnvelope(t, missing, v1.ErrorNotFound)
}

type fakeSQLStateError struct {
	state   string
	message string
}

func (e fakeSQLStateError) Error() string    { return e.message }
func (e fakeSQLStateError) SQLState() string { return e.state }

func mustServer(t *testing.T, repository Repository) *Server {
	t.Helper()
	server, err := New(repository, testAdminToken)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func serve(server http.Handler, method, target, authorization, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func assertErrorEnvelope(t *testing.T, recorder *httptest.ResponseRecorder, code v1.ErrorCode) {
	t.Helper()
	var envelope v1.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.RequestID == "" {
		t.Fatalf("error envelope = %+v; want code %q and request_id", envelope.Error, code)
	}
	if recorder.Header().Get("X-Request-ID") != envelope.Error.RequestID {
		t.Fatalf("X-Request-ID=%q, envelope=%q", recorder.Header().Get("X-Request-ID"), envelope.Error.RequestID)
	}
}
