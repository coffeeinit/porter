// Script-backed kernel builder (FCM-20 remainder): drives the kernel.Build
// state machine around scripts/kernel/build.sh. Phase transitions bracket
// the real script run (fetching → compiling → completed/failed) — coarse
// but truthful; the script's own survival gate enforces Kconfig policy.
// Command is injectable so tests never need a toolchain.
package kernel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ScriptBuilder builds kernels by shelling out to build.sh.
type ScriptBuilder struct {
	Script  string // path to build.sh
	OutDir  string
	Command func(ctx context.Context, script, version, outDir string) *exec.Cmd
}

// command resolves the exec constructor.
func (b ScriptBuilder) command(ctx context.Context, version, outDir string) *exec.Cmd {
	if b.Command != nil {
		return b.Command(ctx, b.Script, version, outDir)
	}
	return exec.CommandContext(ctx, "/bin/sh", b.Script, "--version", version, "--out", outDir)
}

// Validate gates builder config.
func (b ScriptBuilder) Validate() error {
	if b.Script == "" || b.OutDir == "" {
		return fmt.Errorf("kernel: builder needs script and out dir")
	}
	return nil
}

// Run executes the build for version, advancing the Build through its
// states. The same *Build must have been started via Manager.Start.
func (b ScriptBuilder) Run(ctx context.Context, build *Build) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if build == nil {
		return fmt.Errorf("kernel: nil build")
	}
	steps := []struct {
		state    string
		progress int
	}{
		{BuildFetching, 10},
		{BuildConfiguring, 40},
		{BuildCompiling, 70},
	}
	for i, s := range steps {
		if err := build.Advance(s.state, s.progress); err != nil {
			return err
		}
		if i == len(steps)-1 {
			out := filepath.Join(b.OutDir, build.Version)
			if err := os.MkdirAll(out, 0o755); err != nil {
				_ = build.Advance(BuildFailed, s.progress)
				return err
			}
			if err := b.command(ctx, build.Version, out).Run(); err != nil {
				_ = build.Advance(BuildFailed, s.progress)
				return fmt.Errorf("kernel: build script: %w", err)
			}
		}
	}
	return build.Advance(BuildCompleted, 100)
}
