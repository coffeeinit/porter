package main

import (
	"context"
	"errors"
	"fmt"

	"porter/agent/internal/store"
	"porter/protocol"
)

type applyVMFunc func(context.Context, protocol.VMApplySpec) (store.VM, error)
type vmHealthFunc func(store.VM) bool
type vmProcessFunc func(store.VM) (bool, error)

func runVMLifecycleCommand(ctx context.Context, st *store.Store, kind string, spec protocol.VMApplySpec, start, restart applyVMFunc, healthy vmHealthFunc, processRunning vmProcessFunc) (store.VM, error) {
	switch kind {
	case "vm.start":
		return applyVMIdempotent(ctx, st, spec, start, healthy, processRunning)
	case "vm.restart":
		if spec.VMID == "" {
			return store.VM{}, errors.New("vm id required")
		}
		vm, restartErr := restart(ctx, spec)
		if vm.ID == "" {
			vm.ID = spec.VMID
		}
		if vm.ReplicaID == "" {
			vm.ReplicaID = spec.ReplicaID
		}
		if vm.GuestIP == "" {
			vm.GuestIP = spec.GuestIP
		}
		if vm.Port == 0 {
			vm.Port = spec.Port
		}
		if vm.Health == "" {
			vm.Health = "checking"
			if restartErr != nil {
				vm.Health = "unhealthy"
			}
		}
		if restartErr != nil {
			if vm.State == "" {
				vm.State = "failed"
			}
			if vm.Error == "" {
				vm.Error = restartErr.Error()
			}
		} else {
			if vm.State == "" {
				vm.State = "running"
			}
			vm.Error = ""
		}
		if err := st.UpsertVM(vm); err != nil {
			if restartErr != nil {
				return vm, fmt.Errorf("VM restart failed: %v; persist local VM state: %w", restartErr, err)
			}
			return vm, fmt.Errorf("persist restarted VM state: %w", err)
		}
		return vm, restartErr
	default:
		return store.VM{}, fmt.Errorf("unsupported VM lifecycle command %q", kind)
	}
}

// applyVMIdempotent makes a retried vm.apply command safe after the agent has
// launched the guest but the control plane did not receive its first result.
func applyVMIdempotent(ctx context.Context, st *store.Store, spec protocol.VMApplySpec, apply applyVMFunc, healthy vmHealthFunc, processRunning vmProcessFunc) (store.VM, error) {
	if spec.VMID == "" {
		return store.VM{}, errors.New("vm id required")
	}
	if previous, ok := st.GetVM(spec.VMID); ok {
		if previous.State == "running" && healthy(previous) {
			if previous.ReplicaID != "" && spec.ReplicaID != "" && previous.ReplicaID != spec.ReplicaID {
				return previous, fmt.Errorf("vm %q is already running for replica %q", spec.VMID, previous.ReplicaID)
			}
			changed := false
			if previous.ReplicaID == "" && spec.ReplicaID != "" {
				previous.ReplicaID = spec.ReplicaID
				changed = true
			}
			if previous.GuestIP == "" && spec.GuestIP != "" {
				previous.GuestIP = spec.GuestIP
				changed = true
			}
			if previous.Port == 0 && spec.Port != 0 {
				previous.Port = spec.Port
				changed = true
			}
			if previous.Health == "" {
				previous.Health = "checking"
				changed = true
			}
			if changed {
				if err := st.UpsertVM(previous); err != nil {
					return previous, fmt.Errorf("persist existing VM state: %w", err)
				}
			}
			return previous, nil
		}
		if previous.PID <= 0 && (previous.State == "running" || previous.State == "booting" || previous.State == "starting" || previous.State == "stopping") {
			return previous, fmt.Errorf("vm %q is in state %q without a process identity; refusing duplicate apply", spec.VMID, previous.State)
		}
		if previous.PID > 0 {
			if processRunning == nil {
				return previous, fmt.Errorf("vm %q has a saved process identity that cannot be checked; refusing duplicate apply", spec.VMID)
			}
			running, err := processRunning(previous)
			if err != nil {
				return previous, fmt.Errorf("verify existing vm %q before retry: %w", spec.VMID, err)
			}
			if running {
				return previous, fmt.Errorf("vm %q already has a Firecracker process; refusing duplicate apply", spec.VMID)
			}
		}
	}

	vm, applyErr := apply(ctx, spec)
	if vm.ID == "" {
		vm.ID = spec.VMID
	}
	if vm.ReplicaID == "" {
		vm.ReplicaID = spec.ReplicaID
	}
	if vm.GuestIP == "" {
		vm.GuestIP = spec.GuestIP
	}
	if vm.Port == 0 {
		vm.Port = spec.Port
	}
	if vm.Health == "" {
		vm.Health = "checking"
	}
	if applyErr != nil {
		vm.State = "failed"
		vm.Error = applyErr.Error()
		vm.Health = "unhealthy"
	}
	if err := st.UpsertVM(vm); err != nil {
		if applyErr != nil {
			return vm, fmt.Errorf("VM apply failed: %v; persist local VM state: %w", applyErr, err)
		}
		return vm, fmt.Errorf("persist local VM state: %w", err)
	}
	return vm, applyErr
}
