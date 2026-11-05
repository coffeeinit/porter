package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"porter/controlplane/internal/store"
)

func TestWakeAppClampsTargetToPolicyMaximum(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	project, err := st.CreateProject(ctx, "wake-project")
	if err != nil {
		t.Fatal(err)
	}
	app, err := st.CreateApp(ctx, project.ID, "wake-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(ctx, app.ID, app.Image, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAutoscalePolicy(ctx, app.ID, true, 0, 1, 70, 60, false); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	req := httptest.NewRequest("POST", "/v1/apps/"+app.ID+"/wake", nil)
	req.SetPathValue("id", app.ID)
	rec := httptest.NewRecorder()
	s.wakeApp(rec, req)
	if rec.Code != 202 {
		t.Fatalf("wake status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, ok := st.GetApp(ctx, app.ID)
	if !ok || got.ReplicasDesired != 1 {
		t.Fatalf("wake replicas=%d, want max 1", got.ReplicasDesired)
	}
}

func TestWakeTargetHonorsBoundsAndRejectsCorruptPolicy(t *testing.T) {
	for _, tc := range []struct {
		min, max, want int
		bad            bool
	}{{0, 1, 1, false}, {1, 4, 1, false}, {4, 4, 4, false}, {2, 1, 0, true}, {-1, 3, 0, true}} {
		got, err := wakeTarget(store.App{MinReplicas: tc.min, MaxReplicas: tc.max})
		if tc.bad {
			if err == nil {
				t.Errorf("wakeTarget(%d,%d) expected error", tc.min, tc.max)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("wakeTarget(%d,%d)=(%d,%v), want %d", tc.min, tc.max, got, err, tc.want)
		}
	}
}
