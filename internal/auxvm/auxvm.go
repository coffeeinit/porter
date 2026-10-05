// Package auxvm models auxiliary VMs (OCM-20/21): independent Firecracker
// companions to a workload — builders, headless browsers, bastions — paired
// 1:1 with isolation (AllowVMPair), reached through the gateway with per-kind
// access rules (CDP proxy by source IP, WebRTC UDP slot allocation with
// DNAT replay on recovery).
package auxvm

import (
	"fmt"
)

// Kinds of auxiliary VM Porter supports.
const (
	KindBuilder = "builder"
	KindBrowser = "browser"
	KindBastion = "bastion"
)

// AuxVM is one companion VM paired to a workload.
type AuxVM struct {
	ID         string
	WorkloadID string
	Kind       string
	NodeID     string
}

// Validate gates pairing: identity, workload link, and known kind.
func (a AuxVM) Validate() error {
	if a.ID == "" || a.WorkloadID == "" {
		return fmt.Errorf("auxvm: needs id and workload id")
	}
	switch a.Kind {
	case KindBuilder, KindBrowser, KindBastion:
	default:
		return fmt.Errorf("auxvm: unknown kind %q", a.Kind)
	}
	return nil
}

// WebRTC base port and slot count (OCM-21): UDP 56000+N*100, 50 slots.
const (
	WebRTCBasePort = 56000
	WebRTCStride   = 100
	WebRTCSlots    = 50
)

// WebRTCPort allocates the UDP port for slot n.
func WebRTCPort(n int) (int, error) {
	if n < 0 || n >= WebRTCSlots {
		return 0, fmt.Errorf("auxvm: webrtc slot %d out of range", n)
	}
	return WebRTCBasePort + n*WebRTCStride, nil
}

// CDPRule is one headless-browser access rule: only listed source IPs may
// reach the browser's CDP port, and only when paired (AllowVMPair).
type CDPRule struct {
	AuxVMID   string
	Paired    bool // AllowVMPair isolation gate
	AllowedIP []string
}

// Authorize reports whether srcIP may open a CDP session.
func (r CDPRule) Authorize(srcIP string) bool {
	if !r.Paired || srcIP == "" {
		return false
	}
	for _, ip := range r.AllowedIP {
		if ip == srcIP {
			return true
		}
	}
	return false
}
