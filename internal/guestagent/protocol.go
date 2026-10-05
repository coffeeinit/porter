// Package guestagent defines the narrow control contract between Porter and
// the init/agent process inside a Firecracker guest. The transport is injected
// so Unix/vsock implementations can be tested without KVM.
package guestagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const ProtocolVersion = "porter.guest.v1"

type Request struct {
	Protocol string         `json:"protocol"`
	ID       string         `json:"id"`
	Action   string         `json:"action"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type Response struct {
	Protocol string         `json:"protocol"`
	ID       string         `json:"id"`
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type Status struct {
	BootID      string    `json:"boot_id"`
	State       string    `json:"state"` // booting | ready | draining | stopped | failed
	Ready       bool      `json:"ready"`
	Healthy     bool      `json:"healthy"`
	AppPort     int       `json:"app_port,omitempty"`
	CPUPercent  float64   `json:"cpu_percent,omitempty"`
	MemoryBytes uint64    `json:"memory_bytes,omitempty"`
	DiskBytes   uint64    `json:"disk_bytes,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Client struct {
	Dial func(context.Context) (net.Conn, error)
}

func (c Client) call(ctx context.Context, action string, payload map[string]any) (Response, error) {
	if c.Dial == nil {
		return Response{}, fmt.Errorf("guest agent dialer is not configured")
	}
	conn, err := c.Dial(ctx)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	req := Request{Protocol: ProtocolVersion, ID: fmt.Sprintf("%d", time.Now().UnixNano()), Action: action, Payload: payload}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("guest agent request: %w", err)
	}
	var response Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return Response{}, fmt.Errorf("guest agent response: %w", err)
	}
	if response.Protocol != ProtocolVersion || response.ID != req.ID {
		return Response{}, fmt.Errorf("guest agent protocol mismatch")
	}
	if !response.OK {
		return response, fmt.Errorf("guest agent %s: %s", action, response.Error)
	}
	return response, nil
}

func (c Client) Status(ctx context.Context) (Status, error) {
	response, err := c.call(ctx, "status", nil)
	if err != nil {
		return Status{}, err
	}
	b, err := json.Marshal(response.Data)
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(b, &status); err != nil {
		return Status{}, fmt.Errorf("decode guest status: %w", err)
	}
	return status, nil
}

func (c Client) Drain(ctx context.Context) error { _, err := c.call(ctx, "drain", nil); return err }
func (c Client) Shutdown(ctx context.Context) error {
	_, err := c.call(ctx, "shutdown", nil)
	return err
}
func (c Client) Health(ctx context.Context) error { _, err := c.call(ctx, "health", nil); return err }

// ExecResult is the decoded result of one guest exec invocation.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Exec runs argv inside the guest and returns its output. The guest agent
// binary (a future /sbin/porter-agent over vsock) implements the action;
// without it Dial fails and the caller reports "agent not connected".
func (c Client) Exec(ctx context.Context, argv []string, stdin string) (ExecResult, error) {
	response, err := c.call(ctx, "exec", map[string]any{"argv": argv, "stdin": stdin})
	if err != nil {
		return ExecResult{}, err
	}
	b, err := json.Marshal(response.Data)
	if err != nil {
		return ExecResult{}, err
	}
	var result ExecResult
	if err := json.Unmarshal(b, &result); err != nil {
		return ExecResult{}, fmt.Errorf("decode guest exec result: %w", err)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("guest command exited %d: %s", result.ExitCode, result.Stderr)
	}
	return result, nil
}
