package kernel

import (
	"context"
	"os/exec"
	"testing"
)

func stubCmd(ok bool) func(context.Context, string, string, string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ string, _ string) *exec.Cmd {
		if ok {
			return exec.CommandContext(ctx, "go", "version")
		}
		return exec.CommandContext(ctx, "go", "definitely-not-a-subcommand")
	}
}

func TestScriptBuilderRun(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.9")
	if err != nil {
		t.Fatal(err)
	}
	sb := ScriptBuilder{Script: "build.sh", OutDir: t.TempDir(), Command: stubCmd(true)}
	if err := sb.Run(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if b.State != BuildCompleted || b.Progress != 100 {
		t.Fatalf("got %s/%d", b.State, b.Progress)
	}
}

func TestScriptBuilderFailure(t *testing.T) {
	m := NewManager()
	b, _ := m.Start("6.18.9")
	sb := ScriptBuilder{Script: "build.sh", OutDir: t.TempDir(), Command: stubCmd(false)}
	if err := sb.Run(context.Background(), b); err == nil {
		t.Fatal("script failure must surface")
	}
	if b.State != BuildFailed {
		t.Fatalf("build must be failed, got %s", b.State)
	}
}
