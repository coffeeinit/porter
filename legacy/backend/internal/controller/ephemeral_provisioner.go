// Ephemeral provisioner: the real host behind one-shot runs (PVE-08).
// Template/staged image → allocate network → boot → wait for guest agent
// → exec → stop. Every step is best-effort-cleaned: a failed boot still
// attempts Stop so no stranded VM or op survives the run.
package controller

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"porter/internal/netmgr"
	"porter/internal/types"
)

// VMProvisioner is the runtime surface one-shot runs need. *runtime.VMManager
// satisfies it; Boot itself refuses non-KVM hosts, so no separate gate lives
// here.
type VMProvisioner interface {
	NetSpec(vm *types.VM) (netmgr.BootSpec, error)
	Boot(vm *types.VM, spec netmgr.BootSpec) error
	Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error
	Stop(vm *types.VM) error
}

// ImageStager resolves a job image to a staged rootfs file.
type ImageStager interface {
	Stage(image string) (string, error)
}

// DirStager resolves images to <Dir>/<safe-name>.ext4 files staged by the
// operator (base-store releases or converted OCI artifacts). Unknown or
// missing images fail explicitly — never an empty rootfs.
type DirStager struct {
	Dir string
}

// Stage maps an image ref to a staged file, rejecting traversal.
func (d DirStager) Stage(image string) (string, error) {
	if d.Dir == "" {
		return "", fmt.Errorf("ephemeral: no staged-images dir configured")
	}
	safe := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(strings.TrimSpace(image))
	if safe == "" || safe != filepath.Base(safe) || strings.Contains(safe, "..") {
		return "", fmt.Errorf("ephemeral: bad image ref %q", image)
	}
	path := filepath.Join(d.Dir, safe+".ext4")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("ephemeral: image %q not staged (want %s)", image, path)
	}
	return path, nil
}

// ProvisionerHost runs one-shot jobs on real Firecracker VMs.
type ProvisionerHost struct {
	VM           VMProvisioner
	Stage        ImageStager
	ReadyTimeout time.Duration // agent-wait budget; <=0 = 120s
}

// readyTimeout resolves the agent-wait budget.
func (h ProvisionerHost) readyTimeout() time.Duration {
	if h.ReadyTimeout <= 0 {
		return 120 * time.Second
	}
	return h.ReadyTimeout
}

// RunJob boots image, execs cmd, destroys the VM, and returns the guest
// exit code (0 on clean exec; the agent surfaces codes via Exec error).
func (h ProvisionerHost) RunJob(ctx context.Context, image string, cmd []string, memMiB, cores int, _ bool) (int, error) {
	if h.VM == nil || h.Stage == nil {
		return 0, fmt.Errorf("ephemeral: provisioner not configured")
	}
	rootfs, err := h.Stage.Stage(image)
	if err != nil {
		return 0, err
	}
	if memMiB <= 0 {
		memMiB = 256
	}
	if cores <= 0 {
		cores = 1
	}
	vm := &types.VM{
		ID:           fmt.Sprintf("ephem-%d", time.Now().UnixNano()),
		Name:         "ephemeral-run",
		ProjectID:    "ephemeral",
		ReplicaIndex: 0,
		Image:        image,
		RootfsPath:   rootfs,
		VCPUs:        cores,
		MemMiB:       memMiB,
	}
	spec, err := h.VM.NetSpec(vm)
	if err != nil {
		return 0, fmt.Errorf("ephemeral: network: %w", err)
	}
	if err := h.VM.Boot(vm, spec); err != nil {
		_ = h.VM.Stop(vm) // best-effort: never strand a half-booted VM
		return 0, fmt.Errorf("ephemeral: boot: %w", err)
	}
	defer func() { _ = h.VM.Stop(vm) }()

	deadline := time.Now().Add(h.readyTimeout())
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := h.VM.Exec(probeCtx, vm.ID, []string{"true"}, nil, io.Discard)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("ephemeral: guest agent not ready: %w", err)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if err := h.VM.Exec(ctx, vm.ID, cmd, nil, io.Discard); err != nil {
		return 0, fmt.Errorf("ephemeral: exec: %w", err)
	}
	return 0, nil
}
