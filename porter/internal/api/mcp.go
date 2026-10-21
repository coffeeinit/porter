package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"porter/controlplane/internal/imagebuilder"

	"porter/controlplane/internal/store"
	"porter/protocol"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

func (s *Server) mcpInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name": "porter", "version": "v1", "transport": "http-jsonrpc",
		"endpoint": "/mcp", "auth": "Bearer admin token",
	})
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, mcpResponse{JSONRPC: "2.0", Error: map[string]string{"code": "-32700", "message": "parse error"}})
		return
	}
	resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]bool{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "porter", "version": "v1"},
		}
	case "notifications/initialized":
		resp.Result = map[string]string{"status": "ok"}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": porterMCPTools()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		out, err := s.callMCPTool(r, params.Name, params.Arguments)
		if err != nil {
			resp.Result = map[string]interface{}{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}
		} else {
			b, _ := json.MarshalIndent(out, "", "  ")
			resp.Result = map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(b)}}}
		}
	default:
		resp.Error = map[string]string{"code": "-32601", "message": "method not found"}
	}
	writeJSON(w, 200, resp)
}

func porterMCPTools() []map[string]interface{} {
	return []map[string]interface{}{
		{"name": "porter_list_nodes", "description": "List fleet nodes and capacity", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_list_projects", "description": "List projects", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_create_project", "description": "Create a project", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"name"}, "properties": map[string]interface{}{"name": map[string]string{"type": "string"}}}},
		{"name": "porter_create_app", "description": "Create an app in a project", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"project_id", "name", "image"}, "properties": map[string]interface{}{"project_id": map[string]string{"type": "string"}, "name": map[string]string{"type": "string"}, "image": map[string]string{"type": "string"}, "replicas": map[string]string{"type": "integer"}, "port": map[string]string{"type": "integer"}, "vcpus": map[string]string{"type": "integer"}, "memory_mb": map[string]string{"type": "integer"}}}},
		{"name": "porter_deploy", "description": "Create a deployment for an app", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id", "image"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}, "image": map[string]string{"type": "string"}, "replicas": map[string]string{"type": "integer"}}}},
		{"name": "porter_scale_app", "description": "Scale an app's desired replicas", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id", "replicas"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}, "replicas": map[string]string{"type": "integer"}}}},
		{"name": "porter_list_operations", "description": "List recent operations", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_list_volumes", "description": "List project volumes", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"project_id"}, "properties": map[string]interface{}{"project_id": map[string]string{"type": "string"}}}},
		{"name": "porter_create_volume", "description": "Create a project volume", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"project_id", "name", "size_mb"}, "properties": map[string]interface{}{"project_id": map[string]string{"type": "string"}, "name": map[string]string{"type": "string"}, "size_mb": map[string]string{"type": "integer"}}}},
		{"name": "porter_attach_volume", "description": "Attach a volume to an app", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"volume_id", "app_id"}, "properties": map[string]interface{}{"volume_id": map[string]string{"type": "string"}, "app_id": map[string]string{"type": "string"}}}},
		{"name": "porter_list_snapshots", "description": "List VM snapshots", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"vm_id": map[string]string{"type": "string"}}}},
		{"name": "porter_create_snapshot", "description": "Create a VM snapshot", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"vm_id"}, "properties": map[string]interface{}{"vm_id": map[string]string{"type": "string"}}}},
		{"name": "porter_list_logs", "description": "List VM logs", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"vm_id": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
		{"name": "porter_list_metrics", "description": "List fleet metric samples", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"limit": map[string]string{"type": "integer"}}}},
		{"name": "porter_get_autoscale", "description": "Get autoscale/idle policy for an app", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}}}},
		{"name": "porter_set_autoscale", "description": "Set autoscale/idle policy for an app", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}, "enabled": map[string]string{"type": "boolean"}, "min_replicas": map[string]string{"type": "integer"}, "max_replicas": map[string]string{"type": "integer"}, "cpu_target": map[string]string{"type": "integer"}, "idle_timeout_sec": map[string]string{"type": "integer"}, "scale_to_zero": map[string]string{"type": "boolean"}}}},
		{"name": "porter_touch_app", "description": "Report app activity and CPU for autoscaling", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}, "cpu_percent": map[string]string{"type": "number"}}}},
		{"name": "porter_wake_app", "description": "Wake a scaled-to-zero app", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}}}},
		{"name": "porter_list_domains", "description": "List domains", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"project_id": map[string]string{"type": "string"}}}},
		{"name": "porter_create_domain", "description": "Create a domain route", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"project_id", "app_id", "hostname"}, "properties": map[string]interface{}{"project_id": map[string]string{"type": "string"}, "app_id": map[string]string{"type": "string"}, "hostname": map[string]string{"type": "string"}, "kind": map[string]string{"type": "string"}}}},
		{"name": "porter_gateway_stats", "description": "Get gateway traffic stats", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_gateway_requests", "description": "List recent gateway requests", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_list_traces", "description": "List recent gateway traces", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_create_preview", "description": "Create an automatic preview domain", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"app_id", "pr_number"}, "properties": map[string]interface{}{"app_id": map[string]string{"type": "string"}, "pr_number": map[string]string{"type": "integer"}}}},
		{"name": "porter_list_images", "description": "List image catalog", "inputSchema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		{"name": "porter_build_image", "description": "Build Dockerfile/Compose/Git/Docker/OCI into rootfs.ext4", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"name", "source_type"}, "properties": map[string]interface{}{"name": map[string]string{"type": "string"}, "source_type": map[string]string{"type": "string"}, "source_ref": map[string]string{"type": "string"}, "context_dir": map[string]string{"type": "string"}, "dockerfile": map[string]string{"type": "string"}, "git_url": map[string]string{"type": "string"}, "branch": map[string]string{"type": "string"}, "subdir": map[string]string{"type": "string"}, "compose_path": map[string]string{"type": "string"}, "size_mb": map[string]string{"type": "integer"}}}},
		{"name": "porter_detect_framework", "description": "Detect framework in a source directory", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"path"}, "properties": map[string]interface{}{"path": map[string]string{"type": "string"}}}},
		{"name": "porter_create_image", "description": "Register a direct MicroVM or OCI image record", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"name", "source_type", "source_ref"}, "properties": map[string]interface{}{"name": map[string]string{"type": "string"}, "source_type": map[string]string{"type": "string"}, "source_ref": map[string]string{"type": "string"}, "kernel_ref": map[string]string{"type": "string"}, "rootfs_ref": map[string]string{"type": "string"}}}},
		{"name": "porter_resize_volume", "description": "Resize a volume", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"volume_id", "size_mb"}, "properties": map[string]interface{}{"volume_id": map[string]string{"type": "string"}, "size_mb": map[string]string{"type": "integer"}}}},
		{"name": "porter_set_replica_weight", "description": "Set canary/traffic weight for a replica", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"replica_id", "weight"}, "properties": map[string]interface{}{"replica_id": map[string]string{"type": "string"}, "weight": map[string]string{"type": "integer"}}}},
		{"name": "porter_restore_snapshot", "description": "Restore a snapshot", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"snapshot_id"}, "properties": map[string]interface{}{"snapshot_id": map[string]string{"type": "string"}}}},
		{"name": "porter_get_node", "description": "Get fleet node detail", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}}}},
		{"name": "porter_node_metrics", "description": "Get node metric history", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
		{"name": "porter_node_logs", "description": "Get node logs", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
		{"name": "porter_node_vms", "description": "List VMs hosted on a node", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}}}},
		{"name": "porter_cordon_node", "description": "Cordon a fleet node", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}}}},
		{"name": "porter_uncordon_node", "description": "Uncordon a fleet node", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}}}},
		{"name": "porter_drain_node", "description": "Drain a fleet node", "inputSchema": map[string]interface{}{"type": "object", "required": []string{"node_id"}, "properties": map[string]interface{}{"node_id": map[string]string{"type": "string"}}}},
	}
}

func (s *Server) callMCPTool(r *http.Request, name string, raw json.RawMessage) (interface{}, error) {
	ctx := r.Context()
	switch name {
	case "porter_list_nodes":
		return s.Store.ListNodes(ctx)
	case "porter_list_projects":
		return s.Store.ListProjects(ctx)
	case "porter_create_project":
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.CreateProject(ctx, in.Name)
	case "porter_create_app":
		var in struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
			Image     string `json:"image"`
			Replicas  int    `json:"replicas"`
			Port      int    `json:"port"`
			VCPUs     int    `json:"vcpus"`
			MemoryMB  int    `json:"memory_mb"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.CreateApp(ctx, in.ProjectID, in.Name, in.Image, in.Port, in.VCPUs, in.MemoryMB, in.Replicas)
	case "porter_deploy":
		var in struct {
			AppID    string `json:"app_id"`
			Image    string `json:"image"`
			Replicas int    `json:"replicas"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Replicas <= 0 {
			in.Replicas = 1
		}
		return s.Store.CreateDeployment(ctx, in.AppID, in.Image, in.Replicas)
	case "porter_scale_app":
		var in struct {
			AppID    string `json:"app_id"`
			Replicas int    `json:"replicas"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "accepted"}, s.Store.ScaleApp(ctx, in.AppID, in.Replicas)
	case "porter_list_operations":
		return s.Store.ListOperations(ctx)
	case "porter_list_volumes":
		var in struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListVolumes(ctx, in.ProjectID)
	case "porter_create_volume":
		var in struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
			SizeMB    int    `json:"size_mb"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.CreateVolume(ctx, in.ProjectID, in.Name, in.SizeMB)
	case "porter_attach_volume":
		var in struct {
			VolumeID string `json:"volume_id"`
			AppID    string `json:"app_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "attached"}, s.Store.AttachVolume(ctx, in.VolumeID, in.AppID)
	case "porter_list_snapshots":
		var in struct {
			VMID string `json:"vm_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListSnapshots(ctx, in.VMID)
	case "porter_create_snapshot":
		var in protocol.SnapshotSpec
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		op, err := s.Store.QueueVMCommand(ctx, in.VMID, "snapshot.create", "snapshot.create", in)
		return map[string]string{"operation_id": op}, err
	case "porter_list_logs":
		var in struct {
			VMID  string `json:"vm_id"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListLogs(ctx, in.VMID, in.Limit)
	case "porter_list_metrics":
		var in struct {
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListMetrics(ctx, in.Limit)
	case "porter_get_autoscale":
		var in struct {
			AppID string `json:"app_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		a, ok := s.Store.GetApp(ctx, in.AppID)
		if !ok {
			return nil, &mcpToolError{"app not found"}
		}
		return a, nil
	case "porter_set_autoscale":
		var in struct {
			AppID          string `json:"app_id"`
			Enabled        bool   `json:"enabled"`
			MinReplicas    int    `json:"min_replicas"`
			MaxReplicas    int    `json:"max_replicas"`
			CPUTarget      int    `json:"cpu_target"`
			IdleTimeoutSec int    `json:"idle_timeout_sec"`
			ScaleToZero    bool   `json:"scale_to_zero"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "saved"}, s.Store.PutAutoscalePolicy(ctx, in.AppID, in.Enabled, in.MinReplicas, in.MaxReplicas, in.CPUTarget, in.IdleTimeoutSec, in.ScaleToZero)
	case "porter_touch_app":
		var in struct {
			AppID      string  `json:"app_id"`
			CPUPercent float64 `json:"cpu_percent"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, s.Store.TouchApp(ctx, in.AppID, in.CPUPercent)
	case "porter_wake_app":
		var in struct {
			AppID string `json:"app_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		a, ok := s.Store.GetApp(ctx, in.AppID)
		if !ok {
			return nil, &mcpToolError{"app not found"}
		}
		target := a.MinReplicas
		if target < 1 {
			target = 1
		}
		return map[string]interface{}{"status": "waking", "replicas": target}, s.Store.ScaleApp(ctx, a.ID, target)
	case "porter_list_domains":
		var in struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListDomains(ctx, in.ProjectID)
	case "porter_create_domain":
		var in struct {
			ProjectID string `json:"project_id"`
			AppID     string `json:"app_id"`
			Hostname  string `json:"hostname"`
			Kind      string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.CreateDomain(ctx, in.ProjectID, in.AppID, in.Hostname, in.Kind)
	case "porter_gateway_stats":
		return s.Store.GatewayStats(ctx), nil
	case "porter_gateway_requests":
		return s.Store.ListRequestLogs(ctx, 100)
	case "porter_list_traces":
		return s.Store.ListRequestLogs(ctx, 200)
	case "porter_build_image":
		var in struct {
			Name        string `json:"name"`
			SourceType  string `json:"source_type"`
			SourceRef   string `json:"source_ref"`
			ContextDir  string `json:"context_dir"`
			Dockerfile  string `json:"dockerfile"`
			GitURL      string `json:"git_url"`
			Branch      string `json:"branch"`
			Subdir      string `json:"subdir"`
			ComposePath string `json:"compose_path"`
			SizeMB      int    `json:"size_mb"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		b := imagebuilder.New(os.Getenv("PORTER_GUEST_INIT"), os.Getenv("PORTER_KERNEL_IMAGE"), os.Getenv("PORTER_IMAGE_WORKDIR"))
		bctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		var res imagebuilder.Result
		var err error
		switch in.SourceType {
		case "dockerfile":
			res, err = b.BuildDockerfile(bctx, in.ContextDir, in.Dockerfile, in.Name, in.SizeMB)
		case "docker", "oci":
			res, err = b.BuildDockerImage(bctx, in.SourceRef, in.Name, in.SizeMB)
		case "compose":
			res, err = b.BuildCompose(bctx, in.ComposePath, in.Name, in.SizeMB)
		case "git":
			res, err = b.BuildFromGit(bctx, in.GitURL, in.Branch, in.Subdir, in.Name, in.SizeMB)
		default:
			return nil, &mcpToolError{"unsupported source_type"}
		}
		if err != nil {
			return nil, err
		}
		return s.Store.CreateImage(ctx, store.ImageRecord{Name: in.Name, SourceType: in.SourceType, SourceRef: in.SourceRef, Digest: res.Digest, KernelRef: res.KernelPath, RootfsRef: res.RootfsPath, Status: "ready", SizeBytes: res.SizeBytes})
	case "porter_detect_framework":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return imagebuilder.DetectFramework(in.Path)
	case "porter_list_images":
		return s.Store.ListImages(ctx)
	case "porter_create_image":
		var in store.ImageRecord
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.CreateImage(ctx, in)
	case "porter_resize_volume":
		var in struct {
			VolumeID string `json:"volume_id"`
			SizeMB   int    `json:"size_mb"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "resized"}, s.Store.ResizeVolume(ctx, in.VolumeID, in.SizeMB)
	case "porter_set_replica_weight":
		var in struct {
			ReplicaID string `json:"replica_id"`
			Weight    int    `json:"weight"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, s.Store.SetReplicaWeight(ctx, in.ReplicaID, in.Weight)
	case "porter_restore_snapshot":
		var in struct {
			SnapshotID string `json:"snapshot_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		snap, ok := s.Store.GetSnapshot(ctx, in.SnapshotID)
		if !ok {
			return nil, &mcpToolError{"snapshot not found"}
		}
		op, err := s.Store.QueueVMCommand(ctx, snap.VMID, "snapshot.restore", "snapshot.restore", map[string]string{"vm_id": snap.VMID, "state_path": snap.StatePath, "mem_path": snap.MemPath})
		return map[string]string{"operation_id": op}, err
	case "porter_create_preview":
		var in struct {
			AppID    string `json:"app_id"`
			PRNumber int    `json:"pr_number"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if s.BaseDomain == "" {
			return nil, &mcpToolError{"base domain not configured"}
		}
		return s.Store.CreatePreviewDomain(ctx, in.AppID, in.PRNumber, s.BaseDomain)
	case "porter_get_node":
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		n, ok := s.Store.GetNode(ctx, in.NodeID)
		if !ok {
			return nil, &mcpToolError{"node not found"}
		}
		return n, nil
	case "porter_node_metrics":
		var in struct {
			NodeID string `json:"node_id"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListNodeMetrics(ctx, in.NodeID, in.Limit)
	case "porter_node_logs":
		var in struct {
			NodeID string `json:"node_id"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return s.Store.ListNodeLogs(ctx, in.NodeID, in.Limit)
	case "porter_node_vms":
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		vms, err := s.Store.ListNodeVMs(ctx, in.NodeID)
		if err != nil {
			return nil, err
		}
		return publicNodeVMs(vms), nil
	case "porter_cordon_node":
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "cordoned"}, s.Store.SetNodeState(ctx, in.NodeID, "cordoned")
	case "porter_uncordon_node":
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ready"}, s.Store.SetNodeState(ctx, in.NodeID, "ready")
	case "porter_drain_node":
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return map[string]string{"status": "draining"}, s.Store.SetNodeState(ctx, in.NodeID, "draining")
	default:
		return nil, &mcpToolError{name}
	}
}

type mcpToolError struct{ name string }

func (e *mcpToolError) Error() string { return "unknown MCP tool: " + e.name }
