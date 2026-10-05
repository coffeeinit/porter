// Inbound git provider webhooks (GitHub/GitLab/Gitea/generic), PR preview machinery, outbound hooks.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"porter/internal/controller"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Hooks / Crons / Drains / Alerts / Redirects
// ============================================================================

func (a *API) handleListHooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListHooks(a.projectID(r)))
}

func (a *API) handleCreateHook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	h := &types.Hook{ID: store.NewID(), ProjectID: a.projectID(r), Name: req.Name, URL: req.URL, Events: req.Events, Active: true, CreatedAt: time.Now()}
	a.store.PutHook(h)
	writeJSON(w, http.StatusCreated, h)
}

func (a *API) handleDeleteHook(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteHook(r.PathValue("hookId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "hook not found")
}

func (a *API) handleTriggerHook(w http.ResponseWriter, r *http.Request) {
	url := hookURL(a.store.ListHooks(a.projectID(r)), r.PathValue("hookId"))
	if url != "" {
		go postWebhook(url)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "triggered", "hook": r.PathValue("hookId")})
}

func hookURL(hooks []*types.Hook, id string) string {
	for _, h := range hooks {
		if h.ID == id {
			return h.URL
		}
	}
	return ""
}

func postWebhook(url string) error {
	if url == "" {
		return nil
	}
	_, err := http.Post(url, "application/json", strings.NewReader("{}"))
	return err
}

func (a *API) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /webhooks")
}

func (a *API) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /webhooks")
}

func (a *API) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /webhooks/{id}")
}

func (a *API) handlePatchWebhook(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /webhooks/{id}")
}

func (a *API) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /webhooks/{id}")
}

func (a *API) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /webhooks/{id}/test")
}

// ============================================================================
// GitOps webhooks (GitHub/GitLab/Gitea/generic)
// ============================================================================

type githubRepo struct {
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
	FullName string `json:"full_name"`
}

type githubPush struct {
	Ref        string     `json:"ref"`
	Repository githubRepo `json:"repository"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

type githubPR struct {
	Action      string     `json:"action"`
	Number      int        `json:"number"`
	Repository  githubRepo `json:"repository"`
	PullRequest struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
}

func skipCISkip(msg string) (bool, string) { return controller.SkipPreviewBuild(msg) }

func branchOfRef(ref string) string { return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/") }

func previewHost(raw string) string {
	if u := strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://"); u != raw {
		return strings.Split(u, "/")[0]
	}
	return raw
}

func slugBranch(branch string) string {
	b := strings.ToLower(strings.TrimSpace(branch))
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			sb.WriteRune(c)
		default:
			sb.WriteRune('-')
		}
	}
	s := sb.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

func verifyGitHubSignature(secret string, body []byte, sig string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

func gitSettingsOf(settings map[string]any) (repo, secret string, auto bool) {
	if settings == nil {
		return "", "", false
	}
	str := func(k string) string {
		if v, ok := settings[k].(string); ok {
			return v
		}
		return ""
	}
	repo = str("repo")
	if repo == "" {
		repo = str("url")
	}
	secret = str("webhook_secret")
	if b, ok := settings["auto_deploy"].(bool); ok {
		auto = b
	}
	return
}

func repoMatches(settingsRepo, cloneURL, sshURL, fullName string) bool {
	for _, cand := range []string{cloneURL, sshURL, fullName} {
		if cand == "" {
			continue
		}
		if cand == settingsRepo || strings.HasSuffix(cand, "/"+strings.TrimPrefix(settingsRepo, "/")) {
			return true
		}
		if settingsRepo != "" && strings.Contains(cand, settingsRepo) {
			return true
		}
	}
	return false
}

func (a *API) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "", "push":
		a.handleGitHubPush(w, body, sig)
	case "pull_request":
		a.handleGitHubPullRequest(w, body, sig)
	default:
		reason := "unsupported X-GitHub-Event " + event + " (handled: push, pull_request)"
		a.store.AppendDaemonLog("github webhook: " + reason)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": reason})
	}
}

func (a *API) handleGitHubPush(w http.ResponseWriter, body []byte, sig string) {
	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil || push.Ref == "" {
		writeError(w, http.StatusBadRequest, "not a push event")
		return
	}
	branch := branchOfRef(push.Ref)
	if branch == "" {
		writeError(w, http.StatusBadRequest, "no branch in ref")
		return
	}
	if skip, reason := skipCISkip(push.HeadCommit.Message); skip {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "skipped", "reason": reason})
		return
	}
	matched := 0
	previews := []string{}
	for _, proj := range a.store.ListProjects() {
		if proj == nil {
			continue
		}
		repo, secret, auto := gitSettingsOf(a.store.GetProjectSettings(proj.ID, "git"))
		if repo == "" || !repoMatches(repo, push.Repository.CloneURL, push.Repository.SSHURL, push.Repository.FullName) {
			continue
		}
		if !verifyGitHubSignature(secret, body, sig) {
			writeError(w, http.StatusUnauthorized, "bad webhook signature")
			return
		}
		if !auto && branch != "main" && branch != "master" {
			continue
		}
		b := &types.Build{ID: store.NewID(), ProjectID: proj.ID, GitURL: repo, Branch: branch, BuildStatus: "building", CreatedAt: time.Now()}
		a.store.PutBuild(b)
		a.store.AppendBuildLogFor(proj.ID, b.ID, fmt.Sprintf("webhook push %s@%s → build %s", branch, shortHash(push.HeadCommit.ID), b.ID))
		go a.runGitBuild(b, nil, nil, nil)
		preview := a.ensureBranchPreview(proj, branch)
		if preview != "" {
			previews = append(previews, preview)
		}
		matched++
	}
	if matched == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "no matching project"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "building", "previews": previews})
}

func (a *API) handleGitHubPullRequest(w http.ResponseWriter, body []byte, sig string) {
	var pr githubPR
	if err := json.Unmarshal(body, &pr); err != nil || pr.Number == 0 || pr.PullRequest.Head.Ref == "" {
		writeError(w, http.StatusBadRequest, "not a pull_request event")
		return
	}
	switch pr.Action {
	case "opened", "synchronize", "reopened":
		a.buildPRPreview(w, body, sig, &pr)
	case "closed":
		a.teardownPRPreview(w, body, sig, &pr)
	default:
		reason := fmt.Sprintf("pull_request action %q needs no build or teardown", pr.Action)
		a.store.AppendDaemonLog("github webhook: " + reason)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": reason})
	}
}

func (a *API) buildPRPreview(w http.ResponseWriter, body []byte, sig string, pr *githubPR) {
	branch := branchOfRef(pr.PullRequest.Head.Ref)
	if branch == "" {
		writeError(w, http.StatusBadRequest, "no branch in pull_request head ref")
		return
	}
	matched := 0
	previews := []string{}
	for _, proj := range a.store.ListProjects() {
		if proj == nil {
			continue
		}
		repo, secret, auto := gitSettingsOf(a.store.GetProjectSettings(proj.ID, "git"))
		if repo == "" || !repoMatches(repo, pr.Repository.CloneURL, pr.Repository.SSHURL, pr.Repository.FullName) {
			continue
		}
		if !verifyGitHubSignature(secret, body, sig) {
			writeError(w, http.StatusUnauthorized, "bad webhook signature")
			return
		}
		if !auto && branch != "main" && branch != "master" {
			continue
		}
		b := &types.Build{ID: store.NewID(), ProjectID: proj.ID, GitURL: repo, Branch: branch, BuildStatus: "building", CreatedAt: time.Now()}
		a.store.PutBuild(b)
		a.store.AppendBuildLogFor(proj.ID, b.ID, fmt.Sprintf("webhook pull_request #%d %s %s@%s → build %s", pr.Number, pr.Action, branch, shortHash(pr.PullRequest.Head.SHA), b.ID))
		go a.runGitBuild(b, nil, nil, nil)
		preview := a.ensureBranchPreview(proj, branch)
		if preview != "" {
			previews = append(previews, preview)
		}
		matched++
	}
	if matched == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "no matching project"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "building", "pull_request": pr.Number, "previews": previews})
}

func (a *API) teardownPRPreview(w http.ResponseWriter, body []byte, sig string, pr *githubPR) {
	branch := branchOfRef(pr.PullRequest.Head.Ref)
	if branch == "" {
		writeError(w, http.StatusBadRequest, "no branch in pull_request head ref")
		return
	}
	matched := 0
	removed := map[string]int{}
	for _, proj := range a.store.ListProjects() {
		if proj == nil {
			continue
		}
		repo, secret, _ := gitSettingsOf(a.store.GetProjectSettings(proj.ID, "git"))
		if repo == "" || !repoMatches(repo, pr.Repository.CloneURL, pr.Repository.SSHURL, pr.Repository.FullName) {
			continue
		}
		if !verifyGitHubSignature(secret, body, sig) {
			writeError(w, http.StatusUnauthorized, "bad webhook signature")
			return
		}
		matched++
		removed["vms"] += a.deletePRPreviewVMRows(proj.ID, branch)
		removed["domains"] += a.deletePRPreviewDomains(proj, branch)
		removed["environments"] += a.deletePRPreviewEnvironments(proj, branch)
	}
	if matched == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "no matching project"})
		return
	}
	a.store.AppendDaemonLog(fmt.Sprintf("github webhook: pull_request #%d closed → preview teardown (%s): %+v", pr.Number, branch, removed))
	writeJSON(w, http.StatusOK, map[string]any{"status": "torn_down", "pull_request": pr.Number, "removed": removed})
}

func (a *API) deletePRPreviewVMRows(projectID, branch string) int {
	_, bare := a.branchPreviewHosts(projectID, branch)
	if bare == "" {
		return 0
	}
	removed := 0
	for _, d := range a.store.ListDeployments(projectID) {
		if d == nil || d.IsProduction {
			continue
		}
		if strings.ToLower(strings.TrimSpace(d.Environment)) != "preview" {
			continue
		}
		if d.PreviewURL != bare {
			continue
		}
		for _, vmID := range d.VMIDs {
			if _, ok := a.store.GetVM(vmID); ok {
				a.store.DeleteVM(vmID)
				removed++
			}
		}
	}
	return removed
}

func (a *API) deletePRPreviewDomains(proj *types.Project, branch string) int {
	_, bare := a.branchPreviewHosts(proj.ID, branch)
	if bare == "" {
		return 0
	}
	removed := 0
	for _, d := range a.store.ListDomains(proj.ID) {
		if d != nil && d.Type == "preview" && d.Domain == bare {
			a.store.DeleteDomain(proj.ID, d.Domain)
			removed++
		}
	}
	return removed
}

func (a *API) deletePRPreviewEnvironments(proj *types.Project, branch string) int {
	_, bare := a.branchPreviewHosts(proj.ID, branch)
	if bare == "" {
		return 0
	}
	removed := 0
	for _, e := range a.store.ListEnvironments(proj.ID) {
		if e == nil {
			continue
		}
		if e.Branch == branch || e.EnvDomain == bare || previewHost(e.URL) == bare {
			a.store.DeleteEnvironment(e.ID)
			removed++
		}
	}
	return removed
}

func (a *API) branchPreviewHosts(projectID, branch string) (host, bare string) {
	proj, ok := a.store.GetProject(projectID)
	if !ok || proj == nil {
		return "", ""
	}
	slug := slugBranch(branch)
	if slug == "" {
		return "", ""
	}
	if a.baseDomain != "" {
		host = fmt.Sprintf("%s.%s.preview.%s", slug, proj.Name, a.baseDomain)
	} else {
		host = fmt.Sprintf("http://%s-%s.preview.local", slug, proj.Name)
	}
	return host, previewHost(host)
}

func (a *API) ensureBranchPreview(proj *types.Project, branch string) string {
	host, bare := a.branchPreviewHosts(proj.ID, branch)
	if bare == "" {
		return ""
	}
	found := false
	for _, e := range a.store.ListEnvironments(proj.ID) {
		if e != nil && e.Branch == branch {
			found = true
			break
		}
	}
	if !found {
		a.store.PutEnvironment(&types.Environment{
			ID: store.NewID(), ProjectID: proj.ID,
			Name: "preview-" + slugBranch(branch), Branch: branch, URL: host, EnvDomain: bare,
			CreatedAt: time.Now(),
		})
	}
	a.store.AddDomain(proj.ID, &types.Domain{ProjectID: proj.ID, Domain: bare, Type: "preview", Status: "active"})
	return host
}

func (a *API) handleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	a.handleGenericWebhook(w, r)
}

func (a *API) handleGiteaWebhook(w http.ResponseWriter, r *http.Request) {
	a.handleGenericWebhook(w, r)
}

func (a *API) handleGenericWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable body")
		return
	}
	var ev struct {
		Repository struct {
			CloneURL string `json:"clone_url"`
			GitHTTP  string `json:"git_http_url"`
			SSHURL   string `json:"git_ssh_url"`
			FullName string `json:"full_name"`
		} `json:"repository"`
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		writeError(w, http.StatusBadRequest, "unrecognized webhook payload")
		return
	}
	branch := branchOfRef(ev.Ref)
	if branch == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "no branch in payload"})
		return
	}
	matched := 0
	for _, proj := range a.store.ListProjects() {
		if proj == nil {
			continue
		}
		repo, secret, auto := gitSettingsOf(a.store.GetProjectSettings(proj.ID, "git"))
		if repo == "" || !repoMatches(repo, ev.Repository.CloneURL, ev.Repository.GitHTTP, ev.Repository.FullName) {
			continue
		}
		if secret != "" && !verifyGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
			writeError(w, http.StatusUnauthorized, "bad webhook signature")
			return
		}
		if !auto && branch != "main" && branch != "master" {
			continue
		}
		b := &types.Build{ID: store.NewID(), ProjectID: proj.ID, GitURL: repo, Branch: branch, BuildStatus: "building", CreatedAt: time.Now()}
		a.store.PutBuild(b)
		a.store.AppendBuildLogFor(proj.ID, b.ID, "webhook push "+branch+" → build "+b.ID)
		go a.runGitBuild(b, nil, nil, nil)
		matched++
	}
	if matched == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "no matching project"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "building"})
}

func (a *API) handleGetNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	a.notificationGet(w, r, "webhook")
}

func (a *API) handleUpdateNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	a.notificationPut(w, r, "webhook")
}
