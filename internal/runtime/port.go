// Package runtime exposes the stable lifecycle boundary used by the control
// plane. The backend adapter is direct Firecracker; API and orchestration code
// depend on these typed requests and results instead of VMM handles.
package runtime

import (
	"context"
	"fmt"
	"time"

	"porter/internal/types"
)

// NetworkSpec is the runtime-neutral network identity assigned to one replica.
// The Linux networking adapter owns how this identity becomes a TAP, bridge,
// route, or CNI device; the runtime only consumes the resulting contract.
type NetworkSpec struct {
	MACAddress  string
	HostDevice  string
	CIDR        string
	GatewayAddr string
}

// BootRequest is the complete desired boot input for one replica.
type BootRequest struct {
	VM      *types.VM
	Network NetworkSpec
}

// BootResult records the stable identifiers an adapter observed after accepting
// a boot request. A request can be accepted before the guest reaches running;
// the lifecycle worker should use Status for the observed state.
type BootResult struct {
	VMID        string
	RuntimeMode Mode
	ContainerID string // legacy field retained for database compatibility; empty for direct boots
	TaskID      string // legacy field retained for database compatibility; empty for direct boots
	StartedAt   *time.Time
}

// Status is the adapter-neutral observed state of a replica.
type Status struct {
	VMID      string
	State     string
	Health    string
	IPAddress string
	Error     string
}

// Lifecycle is the seam between Porter orchestration and a VM runtime.
// Implementations must make repeated Stop/Delete calls safe and should return
// OperationError values that preserve the operation and VM identity.
type Lifecycle interface {
	Boot(context.Context, BootRequest) (BootResult, error)
	Stop(context.Context, string) error
	Restart(context.Context, string) (BootResult, error)
	Delete(context.Context, string) error
	Status(context.Context, string) (Status, error)
	Close() error
}

// OperationError adds stable context to adapter failures for API responses and
// structured logs without exposing Firecracker implementation details to the
// rest of the control plane.
type OperationError struct {
	Operation string
	VMID      string
	Err       error
}

func (e *OperationError) Error() string {
	if e == nil {
		return ""
	}
	if e.VMID == "" {
		return fmt.Sprintf("runtime %s: %v", e.Operation, e.Err)
	}
	return fmt.Sprintf("runtime %s vm=%s: %v", e.Operation, e.VMID, e.Err)
}

func (e *OperationError) Unwrap() error { return e.Err }
