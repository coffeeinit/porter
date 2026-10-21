package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"porter/controlplane/internal/imagebuilder"
	"porter/controlplane/internal/store"
)

type imageBuildRequest struct {
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

func (s *Server) buildImage(w http.ResponseWriter, r *http.Request) {
	var req imageBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.Name == "" {
		req.Name = "image-" + time.Now().Format("150405")
	}
	if req.SourceType == "" {
		writeError(w, 400, "source_type required")
		return
	}
	img, err := s.Store.CreateImage(r.Context(), store.ImageRecord{Name: req.Name, SourceType: req.SourceType, SourceRef: req.SourceRef, Status: "building"})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.buildMu.Lock()
	s.building[img.ID] = cancel
	s.buildMu.Unlock()
	go func() {
		defer cancel()
		defer func() { s.buildMu.Lock(); delete(s.building, img.ID); s.buildMu.Unlock() }()
		s.runImageBuild(ctx, img.ID, req)
	}()
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"image": img, "status": "building"})
}

func (s *Server) runImageBuild(ctx context.Context, imageID string, req imageBuildRequest) {
	b := imagebuilder.New(os.Getenv("PORTER_GUEST_INIT"), os.Getenv("PORTER_KERNEL_IMAGE"), os.Getenv("PORTER_IMAGE_WORKDIR"))
	b.Log = func(line string) { s.Store.AppendImageLog(context.Background(), imageID, line) }
	var res imagebuilder.Result
	var err error
	switch req.SourceType {
	case "dockerfile":
		if req.ContextDir == "" || req.Dockerfile == "" {
			err = errorString("context_dir and dockerfile required")
			break
		}
		res, err = b.BuildDockerfile(ctx, req.ContextDir, req.Dockerfile, req.Name, req.SizeMB)
	case "docker", "oci":
		if req.SourceRef == "" {
			err = errorString("source_ref required")
			break
		}
		res, err = b.BuildDockerImage(ctx, req.SourceRef, req.Name, req.SizeMB)
	case "compose":
		if req.ComposePath == "" {
			err = errorString("compose_path required")
			break
		}
		res, err = b.BuildCompose(ctx, req.ComposePath, req.Name, req.SizeMB)
	case "git":
		if req.GitURL == "" {
			err = errorString("git_url required")
			break
		}
		res, err = b.BuildFromGit(ctx, req.GitURL, req.Branch, req.Subdir, req.Name, req.SizeMB)
	default:
		err = errorString("unsupported source_type")
	}
	if err != nil {
		s.Store.AppendImageLog(context.Background(), imageID, "ERROR: "+err.Error())
		_ = s.Store.UpdateImage(context.Background(), imageID, "failed", "", "", "", err.Error(), 0)
		return
	}
	_ = s.Store.UpdateImage(context.Background(), imageID, "ready", res.Digest, res.KernelPath, res.RootfsPath, "", res.SizeBytes)
}

func (s *Server) imageLogs(w http.ResponseWriter, r *http.Request) {
	lines, err := s.Store.ListImageLogs(r.Context(), r.PathValue("id"), 1000)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, lines)
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	img, ok := s.Store.GetImage(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, 404, "image not found")
		return
	}
	writeJSON(w, 200, img)
}

func (s *Server) detectFramework(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	d, err := imagebuilder.DetectFramework(req.Path)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

type errorString string

func (e errorString) Error() string { return string(e) }

func (s *Server) cancelImageBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.buildMu.Lock()
	cancel := s.building[id]
	s.buildMu.Unlock()
	if cancel == nil {
		writeError(w, 404, "build not running")
		return
	}
	cancel()
	_ = s.Store.UpdateImage(context.Background(), id, "failed", "", "", "", "build cancelled", 0)
	s.Store.AppendImageLog(context.Background(), id, "build cancelled by operator")
	writeJSON(w, 202, map[string]string{"status": "cancelled"})
}
