package kernel

import (
	"context"
	"strings"
	"testing"
)

func TestScriptBuilderValidateGatesConfig(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := (ScriptBuilder{OutDir: t.TempDir(), Command: stubCmd(true)}).Run(context.Background(), b); err == nil {
		t.Fatal("missing script must fail")
	}
	m2 := NewManager()
	b2, err := m2.Start("6.18.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := (ScriptBuilder{Script: "build.sh", Command: stubCmd(true)}).Run(context.Background(), b2); err == nil {
		t.Fatal("missing out dir must fail")
	}
}

func TestScriptBuilderNilBuild(t *testing.T) {
	sb := ScriptBuilder{Script: "build.sh", OutDir: t.TempDir(), Command: stubCmd(true)}
	if err := sb.Run(context.Background(), nil); err == nil {
		t.Fatal("nil build must fail")
	} else if !strings.Contains(err.Error(), "nil build") {
		t.Fatalf("nil build error wrong: %v", err)
	}
}

func TestScriptBuilderStaleBuildFails(t *testing.T) {
	sb := ScriptBuilder{Script: "build.sh", OutDir: t.TempDir(), Command: stubCmd(true)}
	// Never started via Manager.Start: transition "" -> fetching is illegal.
	if err := sb.Run(context.Background(), &Build{Version: "6.18.9"}); err == nil {
		t.Fatal("unstared build must fail")
	}
}

func TestBuildAdvanceRejectsIllegal(t *testing.T) {
	b := &Build{Version: "6.18.9"}
	if err := b.Advance(BuildCompiling, 70); err == nil {
		t.Fatal("skip-ahead transition must fail")
	}
	if err := b.Advance("bogus-state", 0); err == nil {
		t.Fatal("unknown state must fail")
	}
}

func TestManagerRebuildAfterFailure(t *testing.T) {
	m := NewManager()
	b, err := m.Start("6.18.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Advance(BuildFetching, 10); err != nil {
		t.Fatal(err)
	}
	if err := b.Advance(BuildFailed, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("6.18.9"); err != nil {
		t.Fatalf("failed version may rebuild: %v", err)
	}
}
