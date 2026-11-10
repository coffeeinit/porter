package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"porter/protocol"
)

func releaseFixture(t *testing.T, replicas int) (*Store, context.Context, App) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "release.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateApp(ctx, p.ID, "app", "image:v1", 8080, 1, 128, replicas)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, a
}

func setDeploymentReplicasHealthy(t *testing.T, s *Store, ctx context.Context, dep Deployment) {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM replicas WHERE deployment_id=? ORDER BY id`, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatalf("deployment %s has no replicas", dep.ID)
	}
	for i, id := range ids {
		vm := dep.ID + "-vm-" + string(rune('a'+i))
		if _, err := s.db.ExecContext(ctx, `UPDATE replicas SET vm_id=?,node_id=?,guest_ip=? WHERE id=?`, vm, "node-1", "10.0.0."+string(rune('2'+i)), id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetReplicaHealthByVM(ctx, vm, "running", "healthy", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func activeDeploymentID(t *testing.T, s *Store, appID string) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`SELECT id FROM deployments WHERE app_id=? AND status='active'`, appID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReleasePromotionIsReadinessGated(t *testing.T) {
	s, ctx, a := releaseFixture(t, 1)
	first, err := s.CreateDeployment(ctx, a.ID, "image:v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	setDeploymentReplicasHealthy(t, s, ctx, first)
	if got := activeDeploymentID(t, s, a.ID); got != first.ID {
		t.Fatalf("active=%s want %s", got, first.ID)
	}
	second, err := s.CreateDeployment(ctx, a.ID, "image:v2", 2)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM replicas WHERE deployment_id=?`, second.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("candidate replica count=%d want 2", count)
	}
	traffic := s.HealthyReplicasForApp(ctx, a.ID)
	if len(traffic) != 1 || traffic[0].ReplicaID == "" {
		t.Fatalf("traffic switched before candidate readiness: %+v", traffic)
	}
	if got := activeDeploymentID(t, s, a.ID); got != first.ID {
		t.Fatalf("active before promotion=%s want %s", got, first.ID)
	}
	setDeploymentReplicasHealthy(t, s, ctx, second)
	traffic = s.HealthyReplicasForApp(ctx, a.ID)
	if len(traffic) != 2 {
		t.Fatalf("traffic after promotion has %d replicas, want 2", len(traffic))
	}
	if got := activeDeploymentID(t, s, a.ID); got != second.ID {
		t.Fatalf("active after promotion=%s want %s", got, second.ID)
	}
	var oldState, oldStatus string
	if err := s.db.QueryRow(`SELECT state FROM replicas WHERE deployment_id=? LIMIT 1`, first.ID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if oldState != "deleting" {
		t.Fatalf("former active replica state=%s want deleting", oldState)
	}
	if err := s.db.QueryRow(`SELECT status FROM deployments WHERE id=?`, first.ID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" {
		t.Fatalf("former active status=%s want superseded", oldStatus)
	}
}

func TestFailedCandidateAndConcurrentCandidatePreserveActive(t *testing.T) {
	s, ctx, a := releaseFixture(t, 1)
	first, err := s.CreateDeployment(ctx, a.ID, "image:v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	setDeploymentReplicasHealthy(t, s, ctx, first)
	candidate, err := s.CreateDeployment(ctx, a.ID, "image:v2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, a.ID, "image:v3", 1); err == nil {
		t.Fatal("expected concurrent deployment rejection")
	}
	var vm string
	if err := s.db.QueryRow(`SELECT vm_id FROM replicas WHERE deployment_id=?`, candidate.ID).Scan(&vm); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReplicaHealthByVM(ctx, vm, "failed", "unhealthy", "boot failed"); err != nil {
		t.Fatal(err)
	}
	if got := activeDeploymentID(t, s, a.ID); got != first.ID {
		t.Fatalf("failed candidate replaced active %s", got)
	}
	traffic := s.HealthyReplicasForApp(ctx, a.ID)
	if len(traffic) != 1 {
		t.Fatalf("healthy traffic=%d want former active only", len(traffic))
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM deployments WHERE id=?`, candidate.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("failed candidate status=%s", status)
	}
}

func TestFirstDeploymentActivatesOnlyAfterAllReplicasHealthy(t *testing.T) {
	s, ctx, a := releaseFixture(t, 2)
	d, err := s.CreateDeployment(ctx, a.ID, "image:v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.HealthyReplicasForApp(ctx, a.ID); len(got) != 0 {
		t.Fatalf("premature traffic: %+v", got)
	}
	setDeploymentReplicasHealthy(t, s, ctx, d)
	if got := activeDeploymentID(t, s, a.ID); got != d.ID {
		t.Fatalf("first deployment active=%s want %s", got, d.ID)
	}
	if got := s.HealthyReplicasForApp(ctx, a.ID); len(got) != 2 {
		t.Fatalf("first active replicas=%d want 2", len(got))
	}
}

func TestDeploymentUsesImageRecordKernelAndRootfs(t *testing.T) {
	s, ctx, app := releaseFixture(t, 1)
	now := time.Now().UTC()
	const nodeID = "node-image-release"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO nodes(id,name,os,arch,cpus,memory_mb,disk_mb,cpus_free,memory_free_mb,disk_free_mb,state,token_hash,last_seen,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nodeID, "node", "linux", "amd64", 4, 4096, 50000, 4, 4096, 50000, "ready", "token", now, now); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	image, err := s.CreateImage(ctx, ImageRecord{
		Name: "release-image", SourceType: "git", SourceRef: "https://example.test/repo@0123456789abcdef0123456789abcdef01234567",
		KernelRef: "/artifacts/kernel-v2", RootfsRef: "/artifacts/rootfs-v2", Digest: "sha256:abc",
	})
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	if image.Status != "ready" {
		t.Fatalf("image status=%q, want ready", image.Status)
	}
	if _, err := s.CreateDeployment(ctx, app.ID, "image://"+image.ID, 1); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM commands WHERE kind='vm.apply' ORDER BY created_at DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("read VM apply payload: %v", err)
	}
	var spec protocol.VMApplySpec
	if err := json.Unmarshal([]byte(payload), &spec); err != nil {
		t.Fatalf("decode VM apply payload: %v", err)
	}
	if spec.Image != "image://"+image.ID || spec.KernelPath != image.KernelRef || spec.RootfsPath != image.RootfsRef {
		t.Fatalf("VM spec image refs=(%q, %q, %q), want (%q, %q, %q)", spec.Image, spec.KernelPath, spec.RootfsPath, "image://"+image.ID, image.KernelRef, image.RootfsRef)
	}
}
