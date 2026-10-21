package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"porter/protocol"
)

type Client struct {
	Server    string
	NodeID    string
	NodeToken string
	HTTP      *http.Client
}

func New(server, nodeID, nodeToken string) *Client {
	return &Client{Server: strings.TrimRight(server, "/"), NodeID: nodeID, NodeToken: nodeToken, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func Join(ctx context.Context, server string, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	var out protocol.EnrollResponse
	b, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/v1/fleet/enroll", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return out, fmt.Errorf("enroll: %s", resp.Status)
	}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *Client) do(ctx context.Context, method, path string, in, out interface{}) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.NodeToken)
	req.Header.Set("X-Porter-Node-ID", c.NodeID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Heartbeat(ctx context.Context, hb protocol.Heartbeat) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/heartbeat", hb, nil)
}
func (c *Client) Commands(ctx context.Context) ([]protocol.Command, error) {
	var out []protocol.Command
	err := c.do(ctx, http.MethodGet, "/v1/fleet/commands", nil, &out)
	return out, err
}
func (c *Client) Result(ctx context.Context, res protocol.CommandResult) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/result", res, nil)
}

func (c *Client) PostLogs(ctx context.Context, vmID string, lines []string) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/logs", map[string]interface{}{"vm_id": vmID, "lines": lines}, nil)
}

func (c *Client) RecordSnapshot(ctx context.Context, snap map[string]interface{}) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/snapshot", snap, nil)
}

func (c *Client) ReportVMState(ctx context.Context, vmID, state, health, errText string) error {
	return c.do(ctx, http.MethodPost, "/v1/fleet/vmstate", map[string]string{"vm_id": vmID, "state": state, "health": health, "error": errText}, nil)
}
