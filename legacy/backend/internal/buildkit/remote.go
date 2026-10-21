// Remote buildkitd inside a MicroVM (isolation boundary): pack steps and
// Dockerfile builds run against a builder VM's daemon over TCP instead of
// the host socket, so untrusted build code never executes on the control
// plane (or the agent host userland outside a MicroVM). buildctl streams
// the local context over the connection, so no shared filesystem is needed.
// The builder image bakes `buildkitd --addr tcp://0.0.0.0:1234`; the
// operator stages it like any other base image.
package buildkit

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"porter/internal/netmgr"
	"porter/internal/types"
)

// VMProvisioner boots one-shot infrastructure VMs (satisfied by the
// controller provisioner and the runtime VMManager surface).
type VMProvisioner interface {
	NetSpec(vm *types.VM) (netmgr.BootSpec, error)
	Boot(vm *types.VM, spec netmgr.BootSpec) error
	Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error
	Stop(vm *types.VM) error
}

// DefaultBuilderPort is the TCP port buildkitd listens on inside builder VMs.
const DefaultBuilderPort = 1234

// UseIsolatedVM boots image as a builder VM and points this Builder at its
// daemon. The VM stays up for reuse across builds in the process lifetime;
// stopping it is the caller's job (StopBuilderVM). Porter build orchestration
// must call this method before executing untrusted build work.
func (b *Builder) UseIsolatedVM(ctx context.Context, prov VMProvisioner, image, rootfsPath string, memMiB, port int) error {
	if prov == nil {
		return fmt.Errorf("buildkit: no provisioner for isolated builds")
	}
	if rootfsPath == "" {
		return fmt.Errorf("buildkit: isolated builds need a staged builder rootfs")
	}
	if memMiB <= 0 {
		memMiB = 2048
	}
	if port <= 0 {
		port = DefaultBuilderPort
	}
	vm := &types.VM{
		ID:           fmt.Sprintf("builder-%d", time.Now().UnixNano()),
		Name:         "porter-builder",
		ProjectID:    "system",
		State:        types.StatePending,
		Image:        image,
		RootfsPath:   rootfsPath,
		ReplicaIndex: 0,
		VCPUs:        2,
		MemMiB:       memMiB,
	}
	spec, err := prov.NetSpec(vm)
	if err != nil {
		return fmt.Errorf("buildkit: builder network: %w", err)
	}
	if err := prov.Boot(vm, spec); err != nil {
		_ = prov.Stop(vm)
		return fmt.Errorf("buildkit: builder boot: %w", err)
	}
	ip, _, err := net.ParseCIDR(spec.CIDR)
	if err != nil {
		_ = prov.Stop(vm)
		return fmt.Errorf("buildkit: builder ip: %w", err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := prov.Exec(probe, vm.ID, []string{"true"}, nil, io.Discard)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = prov.Stop(vm)
			return fmt.Errorf("buildkit: builder agent not ready: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = prov.Stop(vm)
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	b.Addr = fmt.Sprintf("tcp://%s:%d", ip.String(), port)
	return nil
}
