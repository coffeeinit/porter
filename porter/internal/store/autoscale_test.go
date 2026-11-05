package store

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func autoscaleTestApp(t *testing.T, withDeployment bool) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, "autoscale-project")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	app, err := s.CreateApp(ctx, project.ID, "autoscale-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if withDeployment {
		if _, err := s.CreateDeployment(ctx, app.ID, app.Image, 1); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	return s, app.ID
}

func TestPutAutoscalePolicyRejectsInvalidBounds(t *testing.T) {
	s, id := autoscaleTestApp(t, false)
	defer s.Close()
	for _, tc := range []struct {
		name                string
		min, max, cpu, idle int
	}{
		{"negative min", -1, 2, 70, 60}, {"zero max", 0, 0, 70, 60}, {"max below min", 2, 1, 70, 60},
		{"cpu low", 0, 2, 0, 60}, {"cpu high", 0, 2, 101, 60}, {"negative idle", 0, 2, 70, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.PutAutoscalePolicy(context.Background(), id, true, tc.min, tc.max, tc.cpu, tc.idle, false); err == nil {
				t.Fatal("expected invalid policy error")
			}
		})
	}
}

func TestReconcileAutoscaleClampsAndReportsScaleFailure(t *testing.T) {
	t.Run("clamps scale up to maximum", func(t *testing.T) {
		s, id := autoscaleTestApp(t, true)
		defer s.Close()
		ctx := context.Background()
		if err := s.PutAutoscalePolicy(ctx, id, true, 1, 2, 50, 3600, false); err != nil {
			t.Fatal(err)
		}
		if err := s.ScaleApp(ctx, id, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE apps SET last_cpu_percent=90 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if err := s.ReconcileAutoscale(ctx); err != nil {
			t.Fatal(err)
		}
		app, ok := s.GetApp(ctx, id)
		if !ok || app.ReplicasDesired != 2 {
			t.Fatalf("replicas=%d, want 2", app.ReplicasDesired)
		}
	})
	t.Run("returns scaling failure", func(t *testing.T) {
		s, id := autoscaleTestApp(t, false)
		defer s.Close()
		ctx := context.Background()
		if err := s.PutAutoscalePolicy(ctx, id, true, 0, 2, 50, 3600, false); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE apps SET last_cpu_percent=90 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		err := s.ReconcileAutoscale(ctx)
		if err == nil || !strings.Contains(err.Error(), "autoscale") {
			t.Fatalf("expected reported scale error, got %v", err)
		}
	})
}

func TestTouchAppRejectsInvalidCPU(t *testing.T) {
	s, id := autoscaleTestApp(t, false)
	defer s.Close()
	for _, cpu := range []float64{-1, 101, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := s.TouchApp(context.Background(), id, cpu); err == nil {
			t.Errorf("TouchApp(%v) accepted", cpu)
		}
	}
}

func TestReconcileAutoscaleRejectsCorruptPolicyAndCorrectsDesired(t *testing.T) {
	t.Run("invalid persisted bounds", func(t *testing.T) {
		s, id := autoscaleTestApp(t, false)
		defer s.Close()
		ctx := context.Background()
		_, _ = s.db.ExecContext(ctx, `UPDATE apps SET autoscale_enabled=1,min_replicas=5,max_replicas=2 WHERE id=?`, id)
		if err := s.ReconcileAutoscale(ctx); err == nil || !strings.Contains(err.Error(), "invalid persisted policy") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("corrects and persists range", func(t *testing.T) {
		s, id := autoscaleTestApp(t, true)
		defer s.Close()
		ctx := context.Background()
		if err := s.PutAutoscalePolicy(ctx, id, true, 2, 3, 70, 60, false); err != nil {
			t.Fatal(err)
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE apps SET replicas_desired=1 WHERE id=?`, id)
		if err := s.ReconcileAutoscale(ctx); err != nil {
			t.Fatal(err)
		}
		a, _ := s.GetApp(ctx, id)
		if a.ReplicasDesired != 2 {
			t.Fatalf("desired=%d want 2", a.ReplicasDesired)
		}
	})
	t.Run("scale zero remains allowed", func(t *testing.T) {
		s, id := autoscaleTestApp(t, true)
		defer s.Close()
		ctx := context.Background()
		if err := s.PutAutoscalePolicy(ctx, id, true, 2, 3, 70, 60, true); err != nil {
			t.Fatal(err)
		}
		if err := s.ScaleApp(ctx, id, 0); err != nil {
			t.Fatal(err)
		}
		if err := s.ReconcileAutoscale(ctx); err != nil {
			t.Fatal(err)
		}
		a, _ := s.GetApp(ctx, id)
		if a.ReplicasDesired != 0 {
			t.Fatalf("desired=%d want 0", a.ReplicasDesired)
		}
	})
}
