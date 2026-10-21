package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"porter/controlplane/internal/imagebuilder"
	"porter/controlplane/internal/store"
	"porter/protocol"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	nodes, _ := s.Store.ListNodes(r.Context())
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "version": protocol.Version, "nodes": len(nodes)})
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	vols, err := s.Store.ListVolumes(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, vols)
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		SizeMB int    `json:"size_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	v, err := s.Store.CreateVolume(r.Context(), r.PathValue("id"), req.Name, req.SizeMB)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) attachVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID string `json:"app_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.AttachVolume(r.Context(), r.PathValue("id"), req.AppID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"status": "attached"})
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.Store.ListSnapshots(r.Context(), r.URL.Query().Get("vm_id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, snaps)
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var req protocol.SnapshotSpec
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.VMID == "" {
		writeError(w, 400, "vm_id required")
		return
	}
	if req.SnapshotID == "" {
		req.SnapshotID = "snap_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	opID, err := s.Store.QueueVMCommand(r.Context(), req.VMID, "snapshot.create", "snapshot.create", req)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": opID, "snapshot_id": req.SnapshotID})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := s.Store.ListLogs(r.Context(), r.URL.Query().Get("vm_id"), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, logs)
}

func (s *Server) ingestLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMID  string   `json:"vm_id"`
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	nodeID := r.Context().Value(nodeIDKey).(string)
	if err := s.Store.AppendLogs(r.Context(), nodeID, req.VMID, req.Lines); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) listMetrics(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	m, err := s.Store.ListMetrics(r.Context(), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	secret := os.Getenv("PORTER_GITHUB_SECRET")
	if secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(want)) {
			writeError(w, 401, "invalid webhook signature")
			return
		}
	}
	var payload map[string]interface{}
	_ = json.Unmarshal(body, &payload)
	event := r.Header.Get("X-GitHub-Event")
	repo := ""
	if rp, ok := payload["repository"].(map[string]interface{}); ok {
		if v, ok := rp["clone_url"].(string); ok {
			repo = v
		}
		if repo == "" {
			if v, ok := rp["html_url"].(string); ok {
				repo = v
			}
		}
	}
	branch := "main"
	if ref, ok := payload["ref"].(string); ok {
		branch = strings.TrimPrefix(ref, "refs/heads/")
	}
	if event == "push" && repo != "" {
		if app, ok := s.Store.FindAppByRepo(r.Context(), repo); ok {
			s.Store.AppendLogs(context.Background(), "github", "", []string{"webhook push: " + repo + " " + branch})
			go s.startGitDeploy(app.ID, repo, branch, app.Name)
			writeJSON(w, 202, map[string]string{"status": "build_queued", "app_id": app.ID})
			return
		}
	}
	s.Store.AppendLogs(r.Context(), "github", "", []string{"github webhook ignored: " + event + " " + repo})
	writeJSON(w, 202, map[string]string{"status": "ignored"})
}

func (s *Server) ingestSnapshot(w http.ResponseWriter, r *http.Request) {
	var snap struct {
		ID        string `json:"id"`
		VMID      string `json:"vm_id"`
		StatePath string `json:"state_path"`
		MemPath   string `json:"mem_path"`
		SizeBytes int64  `json:"size_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.RecordSnapshot(r.Context(), store.SnapshotRecord{ID: snap.ID, VMID: snap.VMID, StatePath: snap.StatePath, MemPath: snap.MemPath, SizeBytes: snap.SizeBytes}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) ingestVMState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VMID   string `json:"vm_id"`
		State  string `json:"state"`
		Health string `json:"health"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := s.Store.SetReplicaHealthByVM(r.Context(), req.VMID, req.State, req.Health, req.Error); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) startGitDeploy(appID, repo, branch, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	b := imagebuilder.New(os.Getenv("PORTER_GUEST_INIT"), os.Getenv("PORTER_KERNEL_IMAGE"), os.Getenv("PORTER_IMAGE_WORKDIR"))
	img, err := s.Store.CreateImage(context.Background(), store.ImageRecord{Name: name + "-" + time.Now().Format("150405"), SourceType: "git", SourceRef: repo, Status: "building"})
	if err != nil {
		return
	}
	b.Log = func(line string) { s.Store.AppendImageLog(context.Background(), img.ID, line) }
	res, err := b.BuildFromGit(ctx, repo, branch, "", img.Name, 512)
	if err != nil {
		s.Store.AppendImageLog(context.Background(), img.ID, "ERROR: "+err.Error())
		_ = s.Store.UpdateImage(context.Background(), img.ID, "failed", "", "", "", err.Error(), 0)
		return
	}
	_ = s.Store.UpdateImage(context.Background(), img.ID, "ready", res.Digest, res.KernelPath, res.RootfsPath, "", res.SizeBytes)
	_ = s.Store.SetAppImage(context.Background(), appID, res.RootfsPath, res.KernelPath)
	_, _ = s.Store.CreateDeployment(context.Background(), appID, "image://"+img.ID, 1)
}
