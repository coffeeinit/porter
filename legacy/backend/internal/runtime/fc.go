// Minimal official Firecracker API client. Firecracker exposes a local HTTP
// API on a Unix domain socket; this client never opens a TCP control port.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type fcClient struct {
	sock string
	http *http.Client
}

func newFCClient(sock string) *fcClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	return &fcClient{sock: sock, http: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
}

func (c *fcClient) put(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s over unix socket %s: %w", path, c.sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("PUT %s: %s", path, resp.Status)
	}
	return nil
}

func (c *fcClient) SetBootSource(ctx context.Context, kernelPath, bootArgs string) error {
	return c.put(ctx, "/boot-source", struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArgs        string `json:"boot_args"`
	}{kernelPath, bootArgs})
}

func (c *fcClient) SetRootDrive(ctx context.Context, path string, readOnly bool) error {
	return c.put(ctx, "/drives/rootfs", struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}{"rootfs", path, true, readOnly})
}

func (c *fcClient) SetDataDrive(ctx context.Context, driveID, path string) error {
	return c.put(ctx, "/drives/"+driveID, struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}{driveID, path, false, false})
}

func (c *fcClient) SetNetworkInterface(ctx context.Context, ifaceID, mac, hostDev string) error {
	return c.put(ctx, "/network-interfaces/"+ifaceID, struct {
		IfaceID     string `json:"iface_id"`
		GuestMAC    string `json:"guest_mac"`
		HostDevName string `json:"host_dev_name"`
	}{ifaceID, mac, hostDev})
}

func (c *fcClient) SetMachineConfig(ctx context.Context, vcpus, memMiB int) error {
	return c.put(ctx, "/machine-config", struct {
		VCPUCount  int `json:"vcpu_count"`
		MemSizeMiB int `json:"mem_size_mib"`
	}{vcpus, memMiB})
}

func (c *fcClient) SetMMDSConfig(ctx context.Context, data map[string]any) error {
	return c.put(ctx, "/mmds/config", struct {
		Version           string         `json:"version"`
		NetworkInterfaces []string       `json:"network_interfaces"`
		MMDSData          map[string]any `json:"mmds_data"`
	}{"V2", []string{"eth0"}, data})
}

func (c *fcClient) InstanceStart(ctx context.Context) error {
	return c.put(ctx, "/actions", struct {
		ActionType string `json:"action_type"`
	}{"InstanceStart"})
}

func (c *fcClient) SetState(ctx context.Context, state string) error {
	return c.patch(ctx, "/vm", struct {
		State string `json:"state"`
	}{state})
}

func (c *fcClient) CreateSnapshot(ctx context.Context, snapshotPath, memFilePath string) error {
	return c.put(ctx, "/snapshot/create", struct {
		SnapshotType string `json:"snapshot_type"`
		SnapshotPath string `json:"snapshot_path"`
		MemFilePath  string `json:"mem_file_path"`
	}{"Full", snapshotPath, memFilePath})
}

func (c *fcClient) LoadSnapshot(ctx context.Context, snapshotPath, memFilePath string, resumeVM bool) error {
	return c.put(ctx, "/snapshot/load", struct {
		SnapshotPath string `json:"snapshot_path"`
		MemBackend   struct {
			BackendPath string `json:"backend_path"`
			BackendType string `json:"backend_type"`
		} `json:"mem_backend"`
		TrackDirtyPages bool `json:"track_dirty_pages"`
		ResumeVM        bool `json:"resume_vm"`
	}{SnapshotPath: snapshotPath, MemBackend: struct {
		BackendPath string `json:"backend_path"`
		BackendType string `json:"backend_type"`
	}{BackendPath: memFilePath, BackendType: "File"}, ResumeVM: resumeVM})
}

func (c *fcClient) patch(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s over unix socket %s: %w", path, c.sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("PATCH %s: %s", path, resp.Status)
	}
	return nil
}
