// Builds, build logs, git import/deploy, sources, git sync.
package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"porter/internal/buildkit"
	"porter/internal/controller"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Git builds
// ============================================================================

func (a *API) handleGitImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GitURL  string            `json:"git_url"`
		Branch  string            `json:"branch"`
		Args    map[string]string `json:"args"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.GitURL == "" {
		writeError(w, http.StatusBadRequest, "git_url is required")
		return
	}
	u, okURL := safeGitURL(req.GitURL)
	if !okURL {
		writeError(w, http.StatusBadRequest, "git_url must be an https:// or git@ repository")
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	b := &types.Build{ID: store.NewID(), ProjectID: a.projectID(r), GitURL: u, Branch: req.Branch, BuildStatus: "building", CreatedAt: time.Now()}
	a.store.PutBuild(b)
	go a.runGitBuild(b, r, req.Args, req.Secrets)
	writeJSON(w, http.StatusAccepted, b)
}

func (a *API) handleDeployGit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repository string            `json:"repository"`
		Branch     string            `json:"branch"`
		Args       map[string]string `json:"args"`
		Secrets    map[string]string `json:"secrets"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	u, okURL := safeGitURL(req.Repository)
	if !okURL {
		writeError(w, http.StatusBadRequest, "repository must be an https:// or git@ URL")
		return
	}
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	d := &types.Deployment{
		ID: store.NewID(), ProjectID: proj.ID, BuildStatus: "building",
		GitURL: u, Environment: "preview", Revision: len(a.store.ListDeployments(proj.ID)) + 1,
		VMIDs: []string{}, CreatedAt: time.Now(),
	}
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, "create deployment: "+err.Error())
		return
	}
	b := &types.Build{ID: store.NewID(), ProjectID: proj.ID, DeploymentID: d.ID, GitURL: u, Branch: req.Branch, BuildStatus: "building", CreatedAt: time.Now()}
	d.BuildID = b.ID
	if err := a.store.CreateDeployment(d); err != nil {
		writeError(w, http.StatusInternalServerError, "link build to deployment: "+err.Error())
		return
	}
	a.store.PutBuild(b)
	a.store.AppendBuildLogFor(proj.ID, b.ID, "git deploy queued (source detection + direct artifact validation)")
	go a.runGitBuild(b, r, req.Args, req.Secrets)
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": d, "build": b})
}

func (a *API) runGitBuild(b *types.Build, r *http.Request, args, secrets map[string]string) {
	a.runGitBuildCtx(b, args, secrets)
	_ = r
}

func (a *API) syncDeploymentFromBuild(b *types.Build) {
	if b == nil || b.DeploymentID == "" {
		return
	}
	d, ok := a.store.GetDeployment(b.ProjectID, b.DeploymentID)
	if !ok {
		return
	}
	d.BuildID = b.ID
	d.GitURL = b.GitURL
	d.GitCommit = b.GitCommit
	d.BuildStatus = b.BuildStatus
	if b.Image != "" {
		d.ImageDigest = b.Image
	}
	if err := a.store.CreateDeployment(d); err != nil {
		a.store.AppendDaemonLog(fmt.Sprintf("deployment %s build sync failed: %v", d.ID, err))
		return
	}
	if a.hub != nil {
		a.hub.Broadcast("deployment.build.updated", map[string]any{
			"deployment": d.ID, "build": b.ID, "status": b.BuildStatus,
			"image": b.Image, "git_commit": b.GitCommit,
		})
	}
}

func (a *API) runGitBuildCtx(b *types.Build, args, secrets map[string]string) {
	if b == nil {
		return
	}
	defer a.syncDeploymentFromBuild(b)
	projID := b.ProjectID
	dir := filepath.Join(os.TempDir(), "porter-build-"+b.ID)
	defer os.RemoveAll(dir)

	logf := func(line string) {
		a.store.AppendBuildLogFor(projID, b.ID, line)
		a.store.AppendDaemonLog("build " + b.ID + ": " + line)
	}

	if err := execShell("git", "clone", "--depth", "1", "--branch", orDefault(b.Branch, "main"), b.GitURL, dir); err != nil {
		logf(fmt.Sprintf("git clone failed: %v", err))
		b.BuildStatus = "failed"
		b.Log += "git clone failed\n"
		a.store.PutBuild(b)
		return
	}
	if commit, err := execOut("git", "-C", dir, "rev-parse", "HEAD"); err == nil {
		b.GitCommit = commit
	} else {
		logf(fmt.Sprintf("could not resolve immutable source revision: %v", err))
		b.BuildStatus = "failed"
		b.Log += "immutable source revision unavailable\n"
		a.store.PutBuild(b)
		return
	}
	logf("repository cloned; looking for direct Firecracker artifacts")
	rootfs := firstExisting(filepath.Join(dir, "rootfs.ext4"), filepath.Join(dir, ".porter", "rootfs.ext4"))
	kernel := firstExisting(filepath.Join(dir, "vmlinux"), filepath.Join(dir, ".porter", "vmlinux"))
	if rootfs == "" || kernel == "" {
		if ociRootfs, ociKernel, ok := a.packFallback(b, dir, logf, args, secrets); ok {
			rootfs, kernel = ociRootfs, ociKernel
		} else {
			b.BuildStatus = "failed"
			b.Log += "direct artifact missing: repository must provide rootfs.ext4 and vmlinux at root or .porter/ (or a Dockerfile/pack the runners can build)\n"
			logf("direct artifact missing — pack fallback also unavailable (see build log)")
			a.store.PutBuild(b)
			return
		}
	}
	if a.customImagesDir == "" {
		b.BuildStatus = "failed"
		b.Log += "custom image storage is not configured\n"
		a.store.PutBuild(b)
		return
	}
	name := "git-" + shortBuildRef(b)
	dest := filepath.Join(a.customImagesDir, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		b.BuildStatus = "failed"
		b.Log += "create direct image directory failed: " + err.Error() + "\n"
		a.store.PutBuild(b)
		return
	}
	if err := copyFile(rootfs, filepath.Join(dest, "rootfs.ext4")); err != nil {
		b.BuildStatus = "failed"
		b.Log += "copy rootfs failed: " + err.Error() + "\n"
		a.store.PutBuild(b)
		return
	}
	if err := copyFile(kernel, filepath.Join(dest, "vmlinux")); err != nil {
		b.BuildStatus = "failed"
		b.Log += "copy kernel failed: " + err.Error() + "\n"
		a.store.PutBuild(b)
		return
	}
	gi := &types.GoldenImage{ID: store.NewID(), Name: name, Image: "custom://" + name, Kind: "custom", Rootfs: filepath.Join(dest, "rootfs.ext4"), Kernel: filepath.Join(dest, "vmlinux"), VCPUs: 1, MemMiB: 256, CreatedAt: time.Now()}
	if sum, err := sha256File(gi.Rootfs); err == nil {
		gi.RootfsSHA256 = sum
	}
	if sum, err := sha256File(gi.Kernel); err == nil {
		gi.KernelSHA256 = sum
	}
	now := time.Now()
	gi.ValidatedAt = &now
	gi.Status = "ready"
	if err := a.store.PutGoldenImage(gi); err != nil {
		b.BuildStatus = "failed"
		b.Log += "save direct image failed: " + err.Error() + "\n"
		a.store.PutBuild(b)
		return
	}
	b.Image = gi.Image
	b.BuildStatus = "ready"
	a.store.PutBuild(b)
	logf(fmt.Sprintf("direct Firecracker image ready: %s (rootfs sha256:%s kernel sha256:%s)", gi.Image, shortHash(gi.RootfsSHA256), shortHash(gi.KernelSHA256)))
	if a.hub != nil {
		a.hub.Broadcast("build.ready", map[string]any{"build": b.ID, "image": gi.Image, "rootfs_sha256": gi.RootfsSHA256, "kernel_sha256": gi.KernelSHA256})
	}
}

func (a *API) packFallback(b *types.Build, dir string, logf func(string), args, secrets map[string]string) (string, string, bool) {
	ociOut := filepath.Join(os.TempDir(), "porter-oci-"+b.ID+".tar")
	plan := buildkit.PlanFor(dir, ociOut)
	plan.Args, plan.Secrets = args, secrets
	logf(fmt.Sprintf("pack detected: engine=%s (building OCI)", plan.Engine))
	if a.hostConfig == nil || a.hostConfig.BuilderVMImage == "" || a.vmProv == nil {
		logf("MicroVM BuildKit required: builder_vm_image and VM provisioner must be configured")
		return "", "", false
	}
	builder := buildkit.Builder{NixpacksBin: a.hostConfig.NixpacksBin, RailpackBin: a.hostConfig.RailpackBin}
	stager := controller.DirStager{Dir: a.hostConfig.GuestBasesDir}
	rootfs, err := stager.Stage(a.hostConfig.BuilderVMImage)
	if err != nil {
		logf(fmt.Sprintf("MicroVM BuildKit builder image unavailable: %v", err))
		return "", "", false
	}
	bctx, bcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer bcancel()
	if err := builder.UseIsolatedVM(bctx, a.vmProv, a.hostConfig.BuilderVMImage, rootfs, 2048, 0); err != nil {
		logf(fmt.Sprintf("MicroVM BuildKit unavailable: %v", err))
		return "", "", false
	}
	logf("MicroVM-isolated BuildKit: " + builder.Addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := builder.BuildWithPlan(ctx, plan); err != nil {
		logf(fmt.Sprintf("pack build failed: %v", err))
		return "", "", false
	}
	rootfsOut := filepath.Join(os.TempDir(), "porter-rootfs-"+b.ID+".ext4")
	res, err := buildkit.ConvertOCIToExt4(ctx, ociOut, rootfsOut, 512)
	if err != nil {
		logf(fmt.Sprintf("OCI→rootfs failed: %v", err))
		return "", "", false
	}
	kernel := ""
	if a.hostConfig != nil {
		kernel = a.hostConfig.KernelImage
	}
	if kernel == "" {
		logf("pack built rootfs but no shared kernel configured (set [firecracker] kernel_image)")
		return "", "", false
	}
	logf(fmt.Sprintf("pack pipeline ready: engine=%s entrypoint=%v cmd=%v", plan.Engine, res.Entrypoint, res.Cmd))
	a.store.RecordUsage(b.ProjectID, "build/"+b.ID, "build.pack", 1, "count", "build:"+b.ID)
	return res.RootfsPath, kernel, true
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func shortBuildRef(b *types.Build) string {
	id := b.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

func (a *API) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, paginate(w, r, a.store.ListBuilds(a.projectID(r))))
}

func (a *API) handleGetBuildByID(w http.ResponseWriter, r *http.Request) {
	b, ok := a.store.GetBuild(r.PathValue("buildId"))
	if !ok || b.ProjectID != a.projectID(r) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *API) handleCancelBuild(w http.ResponseWriter, r *http.Request) {
	b, ok := a.store.GetBuild(r.PathValue("buildId"))
	if !ok || b.ProjectID != a.projectID(r) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	b.BuildStatus = "cancelled"
	a.store.PutBuild(b)
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled", "build": b.ID})
}

func (a *API) handleCreateBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GitURL  string            `json:"git_url"`
		Branch  string            `json:"branch"`
		Args    map[string]string `json:"args"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.GitURL == "" {
		writeError(w, http.StatusBadRequest, "git_url is required")
		return
	}
	u, okURL := safeGitURL(req.GitURL)
	if !okURL {
		writeError(w, http.StatusBadRequest, "git_url must be an https:// or git@ repository")
		return
	}
	b := &types.Build{ID: store.NewID(), ProjectID: a.projectID(r), GitURL: u, Branch: req.Branch, BuildStatus: "building", CreatedAt: time.Now()}
	a.store.PutBuild(b)
	a.store.AppendBuildLogFor(a.projectID(r), b.ID, fmt.Sprintf("build %s started (GitHub source %s@%s) → direct artifact validation", b.ID, req.Branch, u))
	go a.runGitBuild(b, r, req.Args, req.Secrets)
	writeJSON(w, http.StatusAccepted, b)
}

func (a *API) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("buildId")
	b, ok := a.store.GetBuild(buildID)
	if !ok || b.ProjectID != a.projectID(r) {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"build": buildID, "status": b.BuildStatus, "logs": a.store.TailBuildLogsFor(a.projectID(r), buildID, 300)})
}

func (a *API) handleBuildLogStream(w http.ResponseWriter, r *http.Request) {
	projectID, buildID := a.projectID(r), r.PathValue("buildId")
	b, ok := a.store.GetBuild(buildID)
	if !ok || b.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	serveLogStream(w, r, func() logStreamPayload {
		current, found := a.store.GetBuild(buildID)
		if !found {
			return logStreamPayload{Source: "build", Status: "missing"}
		}
		return logStreamPayload{Source: "build", Lines: a.store.TailBuildLogsFor(projectID, buildID, 300), Status: current.BuildStatus}
	}, func(status string) bool { return status == "ready" || status == "failed" })
}

func (a *API) handleGitBranches(w http.ResponseWriter, r *http.Request) {
	proj, ok := a.store.GetProject(a.projectID(r))
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	branches := []string{"main"}
	var url string
	bs := a.store.ListBuilds(proj.ID)
	if len(bs) > 0 {
		url = bs[len(bs)-1].GitURL
	}
	if url != "" {
		if out, err := execOut("git", "ls-remote", "--heads", url); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if i := strings.Index(line, "refs/heads/"); i > 0 {
					branches = append(branches, line[i+len("refs/heads/"):])
				}
			}
		}
	}
	dedup := map[string]bool{}
	uniq := branches[:0]
	for _, b := range branches {
		if !dedup[b] {
			dedup[b] = true
			uniq = append(uniq, b)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": uniq, "project_id": a.projectID(r)})
}

func detectFramework(proj *types.Project) map[string]any {
	ref := strings.ToLower(proj.Image + " " + proj.Source + " " + proj.ComposeYAML)
	detect := func(keys ...string) string {
		for _, k := range keys {
			if strings.Contains(ref, k) {
				return k
			}
		}
		return ""
	}
	type cand struct {
		name    string
		keys    []string
		install string
		build   string
		start   string
	}
	cands := []cand{
		{"node", []string{"node", "nextjs", "nuxt", "sveltekit", "remix"}, "npm install", "npm run build", "npm start"},
		{"nextjs", []string{"next"}, "npm install", "npm run build", "npm run start"},
		{"python", []string{"python", "django", "flask", "fastapi", "streamlit", "jupyter"}, "pip install -r requirements.txt", "", "python app.py"},
		{"go", []string{"golang", "/go", "gobuild", "scratch"}, "go mod download", "go build -o app .", "./app"},
		{"ruby", []string{"ruby", "rails", "jekyll"}, "bundle install", "", "bundle exec rails server"},
		{"php", []string{"php", "laravel", "wordpress"}, "composer install", "", "php artisan serve"},
		{"rust", []string{"rust", "cargo", "actix", "rocket"}, "cargo build --release", "", "./target/release/app"},
		{"deno", []string{"deno"}, "deno cache main.ts", "", "deno run --allow-all main.ts"},
		{"static", []string{"nginx", "httpd", "apache", "static", "caddy", "html", "vite"}, "", "", ""},
		{"postgresql", []string{"postgres", "postgresql"}, "", "", ""},
		{"redis", []string{"redis"}, "", "", ""},
		{"mysql", []string{"mysql", "mariadb"}, "", "", ""},
	}
	for _, c := range cands {
		if detect(c.keys...) != "" {
			return map[string]any{
				"framework":       c.name,
				"install_command": c.install,
				"build_command":   c.build,
				"start_command":   c.start,
				"detected":        true,
			}
		}
	}
	return map[string]any{"framework": "other", "detected": false}
}

func (a *API) handleListSources(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /sources")
}

func (a *API) handleGitHubSource(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /source/github/{uuid}")
}

func (a *API) handleDeleteGitHubSource(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /source/github/{uuid}")
}

func (a *API) handleGitHubSourcePermissions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /source/github/{uuid}/permissions")
}

func (a *API) handleGitHubSourceResources(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /source/github/{uuid}/resources")
}

func (a *API) handleGitLabSource(w http.ResponseWriter, r *http.Request) {
	a.handleGitHubSource(w, r)
}

// recovered from internal/api/handlers.go
func (a *API) handleGitSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	a.store.PutProjectSettings(a.projectID(r), "git", map[string]any{"sync": true, "branch": req.Branch})
	a.store.AppendBuildLog(a.projectID(r), "git sync triggered")
	writeJSON(w, http.StatusOK, map[string]any{"status": "synced", "branch": req.Branch})
}

// recovered from internal/api/handlers.go
func (a *API) handleGitToggles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoDeploy     *bool `json:"auto_deploy"`
		ProductionOnly *bool `json:"production_only"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	toggles := map[string]any{}
	if req.AutoDeploy != nil {
		toggles["auto_deploy"] = *req.AutoDeploy
	}
	if req.ProductionOnly != nil {
		toggles["production_only"] = *req.ProductionOnly
	}
	a.store.PutProjectSettings(a.projectID(r), "git", toggles)
	writeJSON(w, http.StatusOK, toggles)
}
