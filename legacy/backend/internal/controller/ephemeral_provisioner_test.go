package controller

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"porter/internal/netmgr"
	"porter/internal/types"
)

type stubProvisioner struct {
	bootErr error
	execs   int
	stopped int
}

func (s *stubProvisioner) NetSpec(vm *types.VM) (netmgr.BootSpec, error) {
	return netmgr.BootSpec{}, nil
}

func (s *stubProvisioner) Boot(vm *types.VM, spec netmgr.BootSpec) error {
	return s.bootErr
}

func (s *stubProvisioner) Exec(_ context.Context, _ string, _ []string, _ io.Reader, _ io.Writer) error {
	s.execs++
	return nil
}

func (s *stubProvisioner) Stop(vm *types.VM) error {
	s.stopped++
	return nil
}

func stageDir(t *testing.T, name string) DirStager {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name+".ext4"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return DirStager{Dir: dir}
}

func TestProvisionerRuns(t *testing.T) {
	stub := &stubProvisioner{}
	h := ProvisionerHost{VM: stub, Stage: stageDir(t, "alpine")}
	code, err := h.RunJob(context.Background(), "alpine", []string{"echo", "hi"}, 0, 0, false)
	if err != nil || code != 0 {
		t.Fatalf("got %d,%v", code, err)
	}
	if stub.execs < 2 || stub.stopped != 1 {
		t.Fatalf("want probe+exec and one stop, got execs=%d stops=%d", stub.execs, stub.stopped)
	}
}

func TestProvisionerMissingImage(t *testing.T) {
	stub := &stubProvisioner{}
	h := ProvisionerHost{VM: stub, Stage: stageDir(t, "")}
	if _, err := h.RunJob(context.Background(), "ghost", []string{"x"}, 0, 0, false); err == nil {
		t.Fatal("unstaged image must fail")
	}
	if stub.stopped != 0 {
		t.Fatal("nothing booted, nothing to stop")
	}
}

func TestProvisionerBootCleansUp(t *testing.T) {
	stub := &stubProvisioner{bootErr: context.DeadlineExceeded}
	h := ProvisionerHost{VM: stub, Stage: stageDir(t, "alpine")}
	if _, err := h.RunJob(context.Background(), "alpine", []string{"x"}, 0, 0, false); err == nil {
		t.Fatal("boot failure must surface")
	}
	if stub.stopped != 1 {
		t.Fatal("failed boot must still attempt stop")
	}
}

func TestDirStagerTraversal(t *testing.T) {
	d := stageDir(t, "ok")
	for _, bad := range []string{"../evil", "/abs", ""} {
		if _, err := d.Stage(bad); err == nil {
			t.Fatalf("%q must fail", bad)
		}
	}
}
