// Package agent is the privileged host bridge (FC lifecycle, net/storage
// exec, telemetry). Home today: runtime (FCClient/VMManager) plus guestagent
// (vsock exec). A separate agent process is a future privilege boundary, not
// MVP.
package agent
