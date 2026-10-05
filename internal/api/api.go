// Package api implements the Porter Control API.
//
// SINGLE-FILE ROUTE TABLE (2026-10 consolidation).
//
// This file owns:
//   - the API struct + NewAPI
//   - the middleware chain (request-id → auth → rate-limit → CSRF → RBAC → idempotency)
//   - the complete route table (apiRoutes) — one entry per route, all features merged
//   - shared helpers (JSON, ETag, pagination, org/user resolution, CSRF)
//   - core handlers: health/version/csrf/feedback/orgs/groups/projects/users
//
// Handler bodies for anything else live in handlers_impl.go and are grouped by
// resource. There is exactly one `type API` and one `NewAPI`; handler files only
// add methods on *API, never new API types.
//
// Conventions (inviolable):
//   - permission strings are "<resource>.<action>"; never hardcode role names.
//   - GET routes NEVER carry a write/create perm.
//   - self-scoped /users/me/* routes carry "" (identity only, no capability).
//   - auth=false is public-by-design (health, auth, webhook, node enroll);
//     the perm field is ignored on those rows and must be "" for clarity.
//   - literal paths MUST stay above their wildcard siblings in the same group
//     (Go 1.22 ServeMux resolves literals first, but removing one lets the
//     wildcard silently absorb it).
//
// PERMS-MIGRATION — permissions referenced by the table below but not yet
// seeded in migrations/0007_rbac.sql (seed before ship; see the migration
// follow-up for the full list):
//
//	group.read, org.create, org.delete, org.transfer, org.audit, org.settings,
//	org.member.{add,remove,role}, member.{list,invite,role,remove},
//	project.{list,read,create,rename,delete,deploy,restart,scale,settings,
//	          transfer,avatar,network,export,import},
//	env.{list,set}, secret.{list,create,delete,rotate},
//	domain.{list,add,remove,verify}, dnszone.*, dnsrecord.*, certificate.*,
//	replica.{list,start,stop,restart,pause,resume,reboot,snapshot,restore,
//	         delete,exec},
//	console.open, ssh.{connect,toggle}, deployment.{list,create,promote,
//	rollback,freeze}, build.{list,create}, log.read, metric.read,
//	traffic.read, webvital.read, analytics.read,
//	hook.{create,delete,trigger}, cron.{create,update,delete,run},
//	drain.{create,delete}, alert.{create,update,delete,silence,route},
//	alertroute.*, silence.*, incident.*, slo.*, synthetic.*,
//	redirect.{create,delete}, firewall.{create,update,delete},
//	cache.{stats,purge}, volume.{read,create,delete,resize},
//	ssh.toggle, git.{import,settings}, image.{upload,sign,sbom,scan,lineage},
//	registry.*, storageclass.*, objectstore.*, bucket.*, snapshot.*, backup.*,
//	backuppolicy.*, kernel.build, vm.migrate, migration.*,
//	gateway.*, route.*, lb.*, portforward.*,
//	server.{register,remove}, node.{cordon,drain,maintenance,upgrade,recover,
//	replace}, provider.*, region.*, zone.*, nodepool.*, capacity.*,
//	role.*, permission.list, rbac.*, serviceaccount.*, apikey.*,
//	product.*, plan.*, price.*, entitlement.*, subscription.*, invoice.*,
//	payment.*, credit.*, refund.*, tax.*, dunning.*, usage.read,
//	trace.read, audit.{read,export}, workflow.*, workflowrun.*, webhook.*,
//	extension.*, marketplace.*, aiagent.*, aiplan.*, support.impersonate,
//	mcp.{search,describe,call}, template.{read,manage}, share.{create,delete},
//	email.{manage,send}, cf.{tunnel,dns},
//	k8s.cluster.*, k8s.agent.*  (k8s.* = extension, not SRS §40 core).
//
// GO VERSION: idempotency relies on http.Request.Pattern (Go 1.22+).
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"porter/internal/ai"
	"porter/internal/auth"
	"porter/internal/autoscale"
	"porter/internal/billing"
	"porter/internal/buildkit"
	"porter/internal/compose"
	"porter/internal/config"
	"porter/internal/dns"
	"porter/internal/email"
	"porter/internal/event"
	"porter/internal/gateway"
	"porter/internal/imagecatalog"
	"porter/internal/netmgr"
	"porter/internal/notify"
	"porter/internal/ptyd"
	"porter/internal/store"
	"porter/internal/types"
	"porter/internal/volumes"
)

// ============================================================================
// Constants
// ============================================================================

const HeaderOrgID = "X-Porter-Org-Id"
const HeaderUserID = "X-Porter-User-Id"
const healthzTimeout = 2 * time.Second

// ============================================================================
// Interfaces
// ============================================================================

type SnapshotInfo struct {
	SnapshotPath string    `json:"snapshot_path"`
	MemoryPath   string    `json:"memory_path"`
	CreatedAt    time.Time `json:"created_at"`
}

// VMRunner is the executor the API boots replicas through.
type VMRunner interface {
	Boot(ctx context.Context, vm *types.VM) error
	Stop(ctx context.Context, vm *types.VM) error
	Restart(ctx context.Context, vm *types.VM) error
	Delete(ctx context.Context, vm *types.VM) error
	Snapshot(ctx context.Context, vm *types.VM) (SnapshotInfo, error)
	Restore(ctx context.Context, vm *types.VM) error
	Pause(ctx context.Context, vm *types.VM) error
	Resume(ctx context.Context, vm *types.VM) error
	Reboot(ctx context.Context, vm *types.VM) error
}

// WakeProject boots a project's stopped replica, giving the gateway a
// scale-to-zero wake path: a request for a project with nothing running asks
// for a replica to come back instead of only returning 503.
//
// It deliberately does not create capacity. Creating a replica needs an image,
// quota and admission, which is the deployment controller's job; when the
// project has no replica row at all this reports that explicitly rather than
// pretending something was woken.
func WakeProject(ctx context.Context, st *store.Store, runner VMRunner, projectID string) error {
	if st == nil || runner == nil {
		return errors.New("wake: store and runtime are required")
	}
	if projectID == "" {
		return errors.New("wake: project id is required")
	}

	var stopped *types.VM
	for _, vm := range st.ListVMs() {
		if vm == nil || vm.ProjectID != projectID {
			continue
		}
		switch vm.State {
		case types.StateRunning, types.StateBooting, types.StatePending:
			return nil // already up, or already coming up
		case types.StateStopped:
			if stopped == nil {
				stopped = vm
			}
		}
	}
	if stopped == nil {
		return fmt.Errorf("wake: project %s has no stopped replica to wake", projectID)
	}

	stopped.State = types.StateBooting
	st.PutVM(stopped)
	if err := runner.Boot(ctx, stopped); err != nil {
		stopped.State = types.StateFailed
		stopped.Error = "wake boot failed: " + err.Error()
		st.PutVM(stopped)
		return err
	}
	stopped.State = types.StateRunning
	stopped.Error = ""
	st.PutVM(stopped)
	return nil
}

type Execer interface {
	Exec(ctx context.Context, vmID string, argv []string, stdin io.Reader, stdout io.Writer) error
}

type Cataloger interface {
	All() []types.ImageManifest
}

// ============================================================================
// API struct + constructor + setters
// ============================================================================

type API struct {
	store             *store.Store
	hub               *event.Hub
	vmm               VMRunner
	net               *netmgr.NetManager
	catalog           Cataloger
	secretKeyMaterial string
	baseDomain        string
	version           string
	logger            *log.Logger
	hostConfig        *config.Config

	customImagesDir string
	remoteCat       *imagecatalog.RemoteCatalog
	domainMgr       *dns.DomainManager
	volMgr          *volumes.Manager
	vmProv          buildkit.VMProvisioner
	mailer          *notify.Mailer
	mcp             *ai.Server
	emailSvc        *email.Service
	shares          *gateway.Registry
	ptydReg         *ptyd.Registry

	csrfToken string

	rateLimit int
	rateMu    sync.Mutex
	rate      map[string]rateEntry

	jwtKey auth.KeyPair

	autoscaler *autoscale.Scaler
}

func NewAPI(st *store.Store, hub *event.Hub, vmm VMRunner, net *netmgr.NetManager, catalog Cataloger, secretKey, baseDomain, version string) *API {
	return &API{
		store:             st,
		hub:               hub,
		vmm:               vmm,
		net:               net,
		catalog:           catalog,
		secretKeyMaterial: secretKey,
		baseDomain:        baseDomain,
		version:           version,
		logger:            log.New(log.Writer(), "api: ", log.LstdFlags),
		csrfToken:         generateRandomToken(32),
		rate:              map[string]rateEntry{},
	}
}

func (a *API) SetDomainManager(dm *dns.DomainManager)    { a.domainMgr = dm }
func (a *API) SetVolumesManager(vm *volumes.Manager)     { a.volMgr = vm }
func (a *API) SetVMProvisioner(p buildkit.VMProvisioner) { a.vmProv = p }
func (a *API) SetMailer(m *notify.Mailer)                { a.mailer = m }
func (a *API) SetCustomImagesDir(dir string)             { a.customImagesDir = dir }
func (a *API) SetHostConfig(cfg *config.Config)          { a.hostConfig = cfg }

// SetRemoteCatalog attaches the upstream image catalog. Images listed there are
// visible immediately but are only downloaded when pulled, so the binary and
// the checkout stay small.
func (a *API) SetRemoteCatalog(rc *imagecatalog.RemoteCatalog) { a.remoteCat = rc }
func (a *API) SetJWTKey(k auth.KeyPair)                  { a.jwtKey = k }
func (a *API) SetRateLimit(n int)                        { a.rateLimit = n }

func (a *API) StartAutoscaler(interval time.Duration) {
	if a.autoscaler != nil {
		a.StopAutoscaler()
	}
	sc := autoscale.New(a.store,
		func(ctx context.Context, proj *types.Project, idx int) {
			a.bootReplica(proj, createProjectReq{Name: proj.Name, Image: proj.Image, Replicas: 1, Env: proj.Env, Ports: a.projPorts(proj)}, idx)
		},
		func(ctx context.Context, vmID string) {
			if vm, ok := a.store.GetVM(vmID); ok {
				_ = a.vmm.Stop(ctx, vm)
			}
		},
		interval)
	a.autoscaler = sc
	sc.Start()
}

func (a *API) StopAutoscaler() {
	if a.autoscaler != nil {
		a.autoscaler.Stop()
		a.autoscaler = nil
	}
}

// ============================================================================
// Rate limit
// ============================================================================

type rateEntry struct {
	count int
	reset time.Time
}

func (a *API) allowRate(client string) bool {
	ip := client
	if h, _, err := net.SplitHostPort(client); err == nil {
		ip = h
	}
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	e, ok := a.rate[ip]
	if !ok || now.After(e.reset) {
		a.rate[ip] = rateEntry{count: 1, reset: now.Add(time.Minute)}
		return true
	}
	e.count++
	if e.count > a.rateLimit {
		return false
	}
	a.rate[ip] = e
	return true
}

// ============================================================================
// Shared helpers
// ============================================================================

func generateRandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random csrf token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: writeJSON encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"code":       status,
		"message":    msg,
		"error":      msg,
		"request_id": w.Header().Get("X-Request-ID"),
	})
}

func etagOf(v any) string {
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func checkIfMatch(w http.ResponseWriter, r *http.Request, current any) bool {
	want := strings.TrimSpace(r.Header.Get("If-Match"))
	if want == "" || want == "*" {
		return true
	}
	if etagOf(current) == want {
		return true
	}
	writeError(w, http.StatusPreconditionFailed, "resource changed (ETag mismatch; refetch and retry)")
	return false
}

func writeJSONETag(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeJSON(w, http.StatusOK, v)
		return
	}
	if etagFresh(w, r, body) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func selectFields(r *http.Request, v any) any {
	raw := strings.TrimSpace(r.URL.Query().Get("fields"))
	if raw == "" {
		return v
	}
	want := map[string]bool{}
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			want[strings.ToLower(f)] = true
		}
	}
	if len(want) == 0 {
		return v
	}
	body, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return v
	}
	out := map[string]any{}
	for k, val := range m {
		if want[strings.ToLower(k)] {
			out[k] = val
		}
	}
	return out
}

func bearerToken(r *http.Request) string {
	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return r.URL.Query().Get("access_token")
}

// constantTimeEqual — contract: fixed-length inputs only (CSRF = 64 hex chars).
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ============================================================================
// System handlers
// ============================================================================

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": a.version})
}

func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthzTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "version": a.version, "db": "down", "detail": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": a.version, "db": "up", "auth": "bearer"})
}

func (a *API) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    a.version,
		"name":       "porter",
		"engine":     "firecracker",
		"storage":    "postgresql",
		"api_prefix": "",
	})
}

func (a *API) handleCSRFToken(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": a.csrfToken})
}

func (a *API) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject   string `json:"subject"`
		Message   string `json:"message"`
		Category  string `json:"category"`
		ProjectID string `json:"project_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	f := &types.Feedback{
		ID: store.NewID(), Subject: req.Subject, Message: req.Message,
		Category: req.Category, Username: a.userIDFromHeader(r),
		ProjectID: req.ProjectID, CreatedAt: time.Now(),
	}
	a.store.PutFeedback(f)
	a.store.AppendDaemonLog(fmt.Sprintf("feedback %s from %s: %s", req.Category, f.Username, req.Message))
	writeJSON(w, http.StatusCreated, map[string]any{"status": "received", "id": f.ID})
}

func (a *API) handleListFeedback(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, a.store.ListFeedback(n))
}

// ============================================================================
// Org / user context helpers
// ============================================================================

func (a *API) orgIDFromHeader(r *http.Request) string {
	if org := r.Header.Get(HeaderOrgID); org != "" {
		if id, ok := a.store.OrgIDByNameOrID(org); ok {
			return id
		}
		return org
	}
	if orgs := a.store.ListOrgs(); len(orgs) > 0 {
		return orgs[0].ID
	}
	return ""
}

func (a *API) userIDFromHeader(r *http.Request) string {
	p := currentPrincipal(r)
	if p.username == "" {
		return ""
	}
	if uid := r.Header.Get(HeaderUserID); uid != "" && uid != p.username {
		return ""
	}
	return p.username
}

// ============================================================================
// ROUTE TABLE — single source of truth.
// ============================================================================

type routeDef struct {
	method  string
	pattern string
	perm    string
	auth    bool
	handler func(*API, http.ResponseWriter, *http.Request)
}

var apiRoutes = []routeDef{
	// ----------------------------------------------------------------------
	// system (public health/version + csrf)
	// ----------------------------------------------------------------------
	{"GET", "/csrf", "", true, (*API).handleCSRFToken},
	{"GET", "/health", "", false, (*API).handleHealth},
	{"GET", "/healthz", "", false, (*API).handleHealthz},
	{"GET", "/version", "", false, (*API).handleVersion},
	{"GET", "/events", "event.read", true, (*API).handleEvents},

	// ----------------------------------------------------------------------
	// feedback
	// ----------------------------------------------------------------------
	{"POST", "/feedback", "feedback.write", true, (*API).handleFeedback},
	{"GET", "/feedback", "feedback.read", true, (*API).handleListFeedback},

	// ----------------------------------------------------------------------
	// auth (SRS §9)
	// ----------------------------------------------------------------------
	{"POST", "/auth/login", "", false, (*API).handleLogin},
	{"POST", "/login", "", false, (*API).handleLogin},
	{"POST", "/auth/logout", "", true, (*API).handleLogout},
	{"POST", "/logout", "", true, (*API).handleLogout},
	{"POST", "/auth/signup", "", false, (*API).handleSignup},
	{"POST", "/auth/password/forgot", "", false, (*API).handlePasswordForgot},
	{"POST", "/auth/password/reset", "", false, (*API).handlePasswordReset},
	{"POST", "/auth/verify-email", "", false, (*API).handleVerifyEmail},
	{"POST", "/auth/resend-verification", "", true, (*API).handleResendVerification},
	{"GET", "/auth/jwks", "", false, (*API).handleJWKS},
	{"POST", "/auth/ldap/login", "", false, (*API).handleLDAPLogin},
	{"POST", "/auth/token", "", true, (*API).handleMintToken},
	{"GET", "/auth/session", "", true, (*API).handleSession},

	// ----------------------------------------------------------------------
	// users + service accounts (SRS §9)
	// ----------------------------------------------------------------------
	{"GET", "/users/me", "", true, (*API).handleMe},
	{"PATCH", "/users/me", "", true, (*API).handlePatchMe},
	{"DELETE", "/users/me", "", true, (*API).handleDeleteMe},
	{"GET", "/users/me/api-keys", "", true, (*API).handleListAPIKeys},
	{"POST", "/users/me/api-keys", "apikey.create", true, (*API).handleCreateAPIKey},
	{"DELETE", "/users/me/api-keys/{keyId}", "apikey.delete", true, (*API).handleDeleteAPIKey},
	{"GET", "/users", "user.list", true, (*API).handleListUsers},
	{"POST", "/users", "user.create", true, (*API).handleCreateUser},
	{"DELETE", "/users/{username}", "user.delete", true, (*API).handleDeleteUser},

	{"GET", "/service-accounts", "serviceaccount.list", true, (*API).handleListServiceAccounts},
	{"POST", "/service-accounts", "serviceaccount.create", true, (*API).handleCreateServiceAccount},
	{"GET", "/service-accounts/{id}", "serviceaccount.list", true, (*API).handleGetServiceAccount},
	{"PATCH", "/service-accounts/{id}", "serviceaccount.update", true, (*API).handlePatchServiceAccount},
	{"DELETE", "/service-accounts/{id}", "serviceaccount.delete", true, (*API).handleDeleteServiceAccount},
	{"GET", "/service-accounts/{id}/keys", "serviceaccount.list", true, (*API).handleListServiceAccountKeys},
	{"POST", "/service-accounts/{id}/keys", "serviceaccount.create", true, (*API).handleCreateServiceAccountKey},
	{"DELETE", "/service-accounts/{id}/keys/{keyId}", "serviceaccount.delete", true, (*API).handleDeleteServiceAccountKey},

	// ----------------------------------------------------------------------
	// orgs + resellers + customers (SRS §6, §7, §46)
	// ----------------------------------------------------------------------
	{"GET", "/orgs", "", true, (*API).handleListOrgs},
	{"GET", "/orgs/default", "", true, (*API).handleDefaultOrg},
	{"POST", "/orgs", "org.create", true, (*API).handleCreateOrg},
	{"GET", "/orgs/current", "", true, (*API).handleGetCurrentOrg},
	{"PATCH", "/orgs/current", "org.settings", true, (*API).handlePatchCurrentOrg},
	{"DELETE", "/orgs/current", "org.delete", true, (*API).handleDeleteCurrentOrg},
	{"GET", "/orgs/members", "member.list", true, (*API).handleListOrgMembers},
	{"POST", "/orgs/members", "org.member.add", true, (*API).handleAddOrgMember},
	{"PATCH", "/orgs/members/{username}", "org.member.role", true, (*API).handlePatchOrgMember},
	{"DELETE", "/orgs/members/{username}", "org.member.remove", true, (*API).handleRemoveOrgMember},
	{"GET", "/orgs/audit", "org.audit", true, (*API).handleOrgAudit},
	{"POST", "/orgs/transfer", "org.transfer", true, (*API).handleOrgTransfer},
	{"GET", "/orgs/events", "event.read", true, (*API).handleOrgEvents},
	// Reading the current org is a capability-gated read; "/org" is the legacy
	// alias of /orgs/current and had lost its gate ("" = any authenticated
	// principal). Pinned by TestPermForRoute.
	{"GET", "/org", "project.read", true, (*API).handleGetOrg},
	{"PATCH", "/org", "org.settings", true, (*API).handlePatchOrg},

	{"GET", "/resellers", "reseller.list", true, (*API).handleListResellers},
	{"POST", "/resellers", "reseller.create", true, (*API).handleCreateReseller},
	{"GET", "/resellers/{id}", "reseller.list", true, (*API).handleGetReseller},
	{"PATCH", "/resellers/{id}", "reseller.update", true, (*API).handlePatchReseller},
	{"DELETE", "/resellers/{id}", "reseller.delete", true, (*API).handleDeleteReseller},
	{"GET", "/resellers/{id}/customers", "customer.list", true, (*API).handleListResellerCustomers},
	{"POST", "/resellers/{id}/customers", "customer.create", true, (*API).handleAddResellerCustomer},

	{"GET", "/customers", "customer.list", true, (*API).handleListCustomers},
	{"POST", "/customers", "customer.create", true, (*API).handleCreateCustomer},
	{"GET", "/customers/{id}", "customer.list", true, (*API).handleGetCustomer},
	{"PATCH", "/customers/{id}", "customer.update", true, (*API).handlePatchCustomer},
	{"DELETE", "/customers/{id}", "customer.delete", true, (*API).handleDeleteCustomer},

	// ----------------------------------------------------------------------
	// groups (teams) + RBAC (SRS §10)
	// ----------------------------------------------------------------------
	{"GET", "/groups", "group.read", true, (*API).handleListGroups},
	{"POST", "/groups", "group.create", true, (*API).handleCreateGroup},
	{"GET", "/groups/{groupId}", "project.read", true, (*API).handleGetGroup},
	{"PATCH", "/groups/{groupId}", "group.update", true, (*API).handlePatchGroup},
	{"DELETE", "/groups/{groupId}", "group.delete", true, (*API).handleDeleteGroup},
	{"GET", "/groups/{groupId}/projects", "project.read", true, (*API).handleGroupProjects},
	{"POST", "/groups/{groupId}/projects/{projectId}", "project.write", true, (*API).handleAddGroupProject},
	{"DELETE", "/groups/{groupId}/projects/{projectId}", "project.write", true, (*API).handleRemoveGroupProject},
	{"GET", "/groups/{groupId}/members", "member.list", true, (*API).handleListTeamMembers},
	{"POST", "/groups/{groupId}/members", "member.invite", true, (*API).handleAddTeamMember},
	{"DELETE", "/groups/{groupId}/members/{username}", "member.remove", true, (*API).handleRemoveTeamMember},

	{"GET", "/roles", "org.audit", true, (*API).handleListRoles},
	{"POST", "/roles", "org.member.role", true, (*API).handleCreateRole},
	{"GET", "/roles/{roleId}", "org.audit", true, (*API).handleGetRole},
	{"PATCH", "/roles/{roleId}", "org.member.role", true, (*API).handlePatchRole},
	{"DELETE", "/roles/{roleId}", "org.member.role", true, (*API).handleDeleteRole},
	{"GET", "/roles/{roleId}/permissions", "org.audit", true, (*API).handleGetRolePermissions},
	{"PUT", "/roles/{roleId}/permissions", "org.member.role", true, (*API).handleSetRolePermissions},
	{"POST", "/roles/{roleId}/permissions/{permissionId}", "org.member.role", true, (*API).handleAddRolePermission},
	{"DELETE", "/roles/{roleId}/permissions/{permissionId}", "org.member.role", true, (*API).handleRemoveRolePermission},
	{"GET", "/permissions", "org.audit", true, (*API).handleListPermissions},
	{"GET", "/rbac/assignments", "org.audit", true, (*API).handleListRBACAssignments},
	{"POST", "/rbac/assignments", "org.member.role", true, (*API).handleAssignRBACRole},
	{"DELETE", "/rbac/assignments", "org.member.role", true, (*API).handleRevokeRBACRole},

	// ----------------------------------------------------------------------
	// projects (SRS §20)
	// ----------------------------------------------------------------------
	{"GET", "/projects", "project.list", true, (*API).handleListProjects},
	{"POST", "/projects", "project.create", true, (*API).handleCreateProject},
	{"POST", "/projects/compose", "project.create", true, (*API).handleCreateComposeProject},
	{"GET", "/projects/{projectId}", "project.read", true, (*API).handleGetProject},
	{"PATCH", "/projects/{projectId}", "project.rename", true, (*API).handlePatchProject},
	{"DELETE", "/projects/{projectId}", "project.delete", true, (*API).handleDeleteProject},
	{"POST", "/projects/{projectId}/redeploy", "project.deploy", true, (*API).handleRedeployProject},
	{"GET", "/projects/{projectId}/scale", "replica.list", true, (*API).handleGetScale},
	{"PATCH", "/projects/{projectId}/scale", "project.scale", true, (*API).handleScale},
	{"GET", "/projects/{projectId}/healthcheck", "project.read", true, (*API).handleGetHealthcheck},
	{"PUT", "/projects/{projectId}/healthcheck", "project.settings", true, (*API).handlePutHealthcheck},
	{"GET", "/projects/{projectId}/autoscale", "project.read", true, (*API).handleGetAutoscale},
	{"PUT", "/projects/{projectId}/autoscale", "project.settings", true, (*API).handlePutAutoscale},
	{"POST", "/projects/{projectId}/restart", "project.restart", true, (*API).handleRestartProject},

	{"GET", "/projects/{projectId}/env", "env.list", true, (*API).handleListEnv},
	{"POST", "/projects/{projectId}/env", "env.set", true, (*API).handleSetEnv},
	{"POST", "/projects/{projectId}/env/bulk", "env.set", true, (*API).handleSetEnvBulk},
	{"PATCH", "/projects/{projectId}/env/{envId}", "env.set", true, (*API).handlePatchEnv},
	{"DELETE", "/projects/{projectId}/env/{envId}", "env.set", true, (*API).handleDeleteEnv},

	{"GET", "/projects/{projectId}/secrets", "secret.list", true, (*API).handleListSecrets},
	{"POST", "/projects/{projectId}/secrets", "secret.create", true, (*API).handleCreateSecret},
	{"DELETE", "/projects/{projectId}/secrets/{secretId}", "secret.delete", true, (*API).handleDeleteSecret},
	{"GET", "/projects/{projectId}/secrets/{secretId}/versions", "secret.list", true, (*API).handleListSecretVersions},
	{"POST", "/projects/{projectId}/secrets/{secretId}/rotate", "secret.rotate", true, (*API).handleRotateSecret},
	{"POST", "/projects/{projectId}/secrets/{secretId}/rollback/{version}", "secret.rotate", true, (*API).handleRollbackSecret},

	// domains + DNS + TLS (SRS §8, §29, §30)
	{"GET", "/projects/{projectId}/domains", "domain.list", true, (*API).handleListDomains},
	{"POST", "/projects/{projectId}/domains", "domain.add", true, (*API).handleAddDomain},
	{"GET", "/projects/{projectId}/domains/records", "domain.list", true, (*API).handleDomainRecords},
	{"GET", "/projects/{projectId}/domains/{domainId}", "domain.list", true, (*API).handleGetDomain},
	{"DELETE", "/projects/{projectId}/domains/{domainId}", "domain.remove", true, (*API).handleDeleteDomain},
	{"POST", "/projects/{projectId}/domains/{domainId}/verify", "domain.verify", true, (*API).handleVerifyDomain},
	{"POST", "/projects/{projectId}/domains/{domainId}/reverify", "domain.verify", true, (*API).handleVerifyDomain},
	{"POST", "/projects/{projectId}/domains/{domainId}/challenge", "domain.verify", true, (*API).handleDomainChallenge},
	{"POST", "/projects/{projectId}/domains/{domainId}/verify-txt", "domain.verify", true, (*API).handleDomainVerifyTXT},
	{"GET", "/projects/{projectId}/dns", "domain.list", true, (*API).handleProjectDNS},
	{"GET", "/projects/{projectId}/dns/records", "domain.list", true, (*API).handleProjectDNS},
	{"GET", "/domains", "domain.list", true, (*API).handleListOrgDomains},
	{"POST", "/domains/verify", "domain.verify", true, (*API).handleVerifyOrgDomain},

	{"GET", "/dns/zones", "dnszone.list", true, (*API).handleListDNSZones},
	{"POST", "/dns/zones", "dnszone.create", true, (*API).handleCreateDNSZone},
	{"GET", "/dns/zones/{id}", "dnszone.list", true, (*API).handleGetDNSZone},
	{"PATCH", "/dns/zones/{id}", "dnszone.update", true, (*API).handlePatchDNSZone},
	{"DELETE", "/dns/zones/{id}", "dnszone.delete", true, (*API).handleDeleteDNSZone},
	{"GET", "/dns/zones/{id}/records", "dnsrecord.list", true, (*API).handleListDNSRecords},
	{"POST", "/dns/zones/{id}/records", "dnsrecord.create", true, (*API).handleCreateDNSRecord},
	{"GET", "/dns/zones/{id}/records/{rid}", "dnsrecord.list", true, (*API).handleGetDNSRecord},
	{"PATCH", "/dns/zones/{id}/records/{rid}", "dnsrecord.update", true, (*API).handlePatchDNSRecord},
	{"DELETE", "/dns/zones/{id}/records/{rid}", "dnsrecord.delete", true, (*API).handleDeleteDNSRecord},
	{"POST", "/dns/zones/{id}/verify", "dnszone.verify", true, (*API).handleVerifyDNSZone},
	{"POST", "/dns/zones/{id}/propagate", "dnszone.verify", true, (*API).handlePropagateDNSZone},

	{"GET", "/certificates", "certificate.list", true, (*API).handleListCertificates},
	{"POST", "/certificates", "certificate.create", true, (*API).handleCreateCertificate},
	{"GET", "/certificates/{id}", "certificate.list", true, (*API).handleGetCertificate},
	{"PATCH", "/certificates/{id}", "certificate.update", true, (*API).handlePatchCertificate},
	{"DELETE", "/certificates/{id}", "certificate.delete", true, (*API).handleDeleteCertificate},
	{"POST", "/certificates/{id}/issue", "certificate.issue", true, (*API).handleIssueCertificate},
	{"POST", "/certificates/{id}/renew", "certificate.renew", true, (*API).handleRenewCertificate},
	{"POST", "/certificates/{id}/revoke", "certificate.revoke", true, (*API).handleRevokeCertificate},
	{"GET", "/certificates/{id}/deployments", "certificate.list", true, (*API).handleListCertificateDeployments},

	// gateway + LB (SRS §28)
	{"GET", "/gateways", "gateway.list", true, (*API).handleListGateways},
	{"POST", "/gateways", "gateway.create", true, (*API).handleCreateGateway},
	{"GET", "/gateways/{id}", "gateway.list", true, (*API).handleGetGateway},
	{"PATCH", "/gateways/{id}", "gateway.update", true, (*API).handlePatchGateway},
	{"DELETE", "/gateways/{id}", "gateway.delete", true, (*API).handleDeleteGateway},
	{"GET", "/gateways/{id}/routes", "route.list", true, (*API).handleListGatewayRoutes},
	{"POST", "/gateways/{id}/routes", "route.create", true, (*API).handleCreateGatewayRoute},
	{"GET", "/gateways/{id}/routes/{rid}", "route.list", true, (*API).handleGetGatewayRoute},
	{"PATCH", "/gateways/{id}/routes/{rid}", "route.update", true, (*API).handlePatchGatewayRoute},
	{"DELETE", "/gateways/{id}/routes/{rid}", "route.delete", true, (*API).handleDeleteGatewayRoute},
	{"GET", "/gateways/{id}/health", "gateway.list", true, (*API).handleGatewayHealth},
	{"GET", "/load-balancers", "lb.list", true, (*API).handleListLoadBalancers},
	{"POST", "/load-balancers", "lb.create", true, (*API).handleCreateLoadBalancer},
	{"GET", "/load-balancers/{id}", "lb.list", true, (*API).handleGetLoadBalancer},
	{"PATCH", "/load-balancers/{id}", "lb.update", true, (*API).handlePatchLoadBalancer},
	{"DELETE", "/load-balancers/{id}", "lb.delete", true, (*API).handleDeleteLoadBalancer},
	{"GET", "/port-forwards", "portforward.list", true, (*API).handleListPortForwards},
	{"POST", "/port-forwards", "portforward.create", true, (*API).handleCreatePortForward},
	{"DELETE", "/port-forwards/{id}", "portforward.delete", true, (*API).handleDeletePortForward},

	// compose + project subtree
	{"GET", "/projects/{projectId}/compose", "project.read", true, (*API).handleGetCompose},
	{"PUT", "/projects/{projectId}/compose", "project.import", true, (*API).handlePutCompose},
	{"POST", "/projects/{projectId}/compose/validate", "project.import", true, (*API).handleValidateCompose},
	{"GET", "/projects/{projectId}/compose/preview", "project.read", true, (*API).handleComposePreview},
	{"GET", "/projects/{projectId}/logs", "log.read", true, (*API).handleProjectLogs},
	{"GET", "/projects/{projectId}/logs/stream", "log.read", true, (*API).handleProjectLogStream},
	{"GET", "/projects/{projectId}/metrics", "metric.read", true, (*API).handleProjectMetrics},
	{"GET", "/projects/{projectId}/traffic", "traffic.read", true, (*API).handleProjectTraffic},
	{"GET", "/projects/{projectId}/events", "event.read", true, (*API).handleProjectEvents},
	{"GET", "/projects/{projectId}/pool", "replica.list", true, (*API).handlePoolStatus},
	{"POST", "/projects/{projectId}/pool/drain", "project.settings", true, (*API).handlePoolDrain},
	{"GET", "/projects/{projectId}/status", "project.read", true, (*API).handleProjectStatus},
	{"GET", "/projects/{projectId}/liveness", "project.read", true, (*API).handleProjectLiveness},

	// replicas (SRS §12)
	{"GET", "/projects/{projectId}/replicas", "replica.list", true, (*API).handleListReplicas},
	{"POST", "/projects/{projectId}/replicas/batch/start", "replica.start", true, (*API).handleReplicaBatchStart},
	{"POST", "/projects/{projectId}/replicas/batch/stop", "replica.stop", true, (*API).handleReplicaBatchStop},
	{"GET", "/projects/{projectId}/replicas/{n}", "replica.list", true, (*API).handleGetReplica},
	{"POST", "/projects/{projectId}/replicas/{n}/start", "replica.start", true, (*API).handleReplicaStart},
	{"POST", "/projects/{projectId}/replicas/{n}/stop", "replica.stop", true, (*API).handleReplicaStop},
	{"POST", "/projects/{projectId}/replicas/{n}/restart", "replica.restart", true, (*API).handleReplicaRestart},
	{"POST", "/projects/{projectId}/replicas/{n}/pause", "replica.pause", true, (*API).handleReplicaPause},
	{"POST", "/projects/{projectId}/replicas/{n}/resume", "replica.resume", true, (*API).handleReplicaResume},
	{"POST", "/projects/{projectId}/replicas/{n}/reboot", "replica.reboot", true, (*API).handleReplicaReboot},
	{"POST", "/projects/{projectId}/replicas/{n}/snapshot", "replica.snapshot", true, (*API).handleReplicaSnapshot},
	{"POST", "/projects/{projectId}/replicas/{n}/restore", "replica.restore", true, (*API).handleReplicaRestore},
	{"POST", "/projects/{projectId}/replicas/{n}/recover", "replica.restore", true, (*API).handleReplicaRestore},
	{"DELETE", "/projects/{projectId}/replicas/{n}", "replica.delete", true, (*API).handleReplicaDelete},
	{"GET", "/projects/{projectId}/replicas/{n}/logs", "log.read", true, (*API).handleReplicaLogs},
	{"GET", "/projects/{projectId}/replicas/{n}/logs/search", "log.read", true, (*API).handleSearchReplicaLogs},
	{"GET", "/projects/{projectId}/replicas/{n}/metrics", "metric.read", true, (*API).handleReplicaMetrics},
	{"GET", "/projects/{projectId}/replicas/{n}/traffic", "traffic.read", true, (*API).handleReplicaTraffic},
	{"GET", "/projects/{projectId}/replicas/{n}/health", "replica.list", true, (*API).handleReplicaHealth},
	{"GET", "/projects/{projectId}/replicas/{n}/ssh-info", "ssh.connect", true, (*API).handleSSHInfo},
	{"POST", "/projects/{projectId}/replicas/{n}/ssh-cert", "ssh.connect", true, (*API).handleSSHCert},
	{"POST", "/projects/{projectId}/replicas/{n}/exec", "replica.exec", true, (*API).handleReplicaExec},
	{"GET", "/projects/{projectId}/replicas/{n}/console", "console.open", true, (*API).handleReplicaConsole},
	{"POST", "/projects/{projectId}/replicas/{n}/console-token", "console.open", true, (*API).handleMintConsoleToken},

	// disruption budgets (SRS §58)
	{"GET", "/projects/{projectId}/settings/disruption-budget", "project.read", true, (*API).handleGetDisruptionBudget},
	{"PUT", "/projects/{projectId}/settings/disruption-budget", "project.settings", true, (*API).handlePutDisruptionBudget},

	// deployments (SRS §21)
	{"GET", "/projects/{projectId}/deployments", "deployment.list", true, (*API).handleListDeployments},
	{"POST", "/projects/{projectId}/deployments", "deployment.create", true, (*API).handleCreateDeployment},
	{"POST", "/projects/{projectId}/deployments/git", "build.create", true, (*API).handleDeployGit},
	{"GET", "/projects/{projectId}/deployments/upload", "deployment.create", true, (*API).handleDeploymentUpload},
	{"GET", "/projects/{projectId}/deployments/{deployId}", "deployment.list", true, (*API).handleGetDeployment},
	{"GET", "/projects/{projectId}/deployments/{deployId}/checks", "deployment.list", true, (*API).handleGetDeploymentChecks},
	{"PUT", "/projects/{projectId}/deployments/{deployId}/checks", "deployment.create", true, (*API).handleUpsertDeploymentChecks},
	{"PATCH", "/projects/{projectId}/deployments/{deployId}/checks/{checkName}", "deployment.create", true, (*API).handleSetDeploymentCheck},
	{"PUT", "/projects/{projectId}/deployments/{deployId}/rollout", "deployment.promote", true, (*API).handleSetDeploymentRollout},
	{"GET", "/projects/{projectId}/deployments/{deployId}/logs", "log.read", true, (*API).handleDeploymentLogs},
	{"POST", "/projects/{projectId}/deployments/{deployId}/promote", "deployment.promote", true, (*API).handlePromoteDeployment},
	{"POST", "/projects/{projectId}/deployments/{deployId}/rollback", "deployment.rollback", true, (*API).handleRollbackDeployment},
	{"DELETE", "/projects/{projectId}/deployments/{deployId}", "deployment.rollback", true, (*API).handleDeleteDeployment},
	{"GET", "/projects/{projectId}/deployments/{deployId}/source", "deployment.list", true, (*API).handleDeploymentSource},
	{"GET", "/projects/{projectId}/deployments/{deployId}/og", "deployment.list", true, (*API).handleDeploymentOG},
	{"POST", "/projects/{projectId}/deployments/freeze", "deployment.freeze", true, (*API).handleFreezeDeployments},
	{"POST", "/projects/{projectId}/deployments/unfreeze", "deployment.freeze", true, (*API).handleUnfreezeDeployments},
	{"GET", "/projects/{projectId}/deployments/freeze-status", "deployment.list", true, (*API).handleFreezeStatus},

	// previews (SRS §25)
	{"GET", "/projects/{projectId}/previews", "project.read", true, (*API).handleListPreviews},
	{"POST", "/projects/{projectId}/previews", "project.deploy", true, (*API).handleCreatePreview},
	{"GET", "/projects/{projectId}/previews/{pr}", "project.read", true, (*API).handleGetPreview},
	{"DELETE", "/projects/{projectId}/previews/{pr}", "project.delete", true, (*API).handleDeletePreview},
	{"POST", "/projects/{projectId}/previews/{pr}/teardown", "project.delete", true, (*API).handleTeardownPreview},
	{"GET", "/projects/{projectId}/previews/{pr}/logs", "log.read", true, (*API).handlePreviewLogs},

	// project settings subtree
	{"GET", "/projects/{projectId}/settings/general", "project.read", true, (*API).handleGetGeneral},
	{"PATCH", "/projects/{projectId}/settings/general", "project.settings", true, (*API).handlePatchGeneral},
	{"POST", "/projects/{projectId}/avatar", "project.avatar", true, (*API).handleSetAvatar},
	{"POST", "/projects/{projectId}/transfer", "project.transfer", true, (*API).handleTransferProject},
	{"GET", "/projects/{projectId}/settings/build", "project.read", true, (*API).handleGetBuild},
	{"PUT", "/projects/{projectId}/settings/build", "project.settings", true, (*API).handlePutBuild},
	{"GET", "/projects/{projectId}/settings/checks", "project.read", true, (*API).handleGetChecks},
	{"POST", "/projects/{projectId}/settings/checks", "project.settings", true, (*API).handlePutChecks},
	{"GET", "/projects/{projectId}/settings/rollout", "project.read", true, (*API).handleGetRollout},
	{"PUT", "/projects/{projectId}/settings/rollout", "project.settings", true, (*API).handlePutRollout},
	{"GET", "/projects/{projectId}/settings/build-machine", "project.read", true, (*API).handleGetBuildMachine},
	{"PUT", "/projects/{projectId}/settings/build-machine", "project.settings", true, (*API).handlePutBuildMachine},
	{"POST", "/projects/{projectId}/settings/ignore-command", "git.settings", true, (*API).handleSetIgnoreCommand},
	{"GET", "/projects/{projectId}/settings/framework", "project.read", true, (*API).handleGetFramework},
	{"GET", "/projects/{projectId}/settings/git", "project.read", true, (*API).handleGetGit},
	{"PUT", "/projects/{projectId}/settings/git", "git.settings", true, (*API).handlePutGit},
	{"POST", "/projects/{projectId}/settings/git/sync", "git.settings", true, (*API).handleGitSync},
	{"PATCH", "/projects/{projectId}/settings/git/toggles", "git.settings", true, (*API).handleGitToggles},
	{"GET", "/projects/{projectId}/settings/git/lfs", "project.read", true, (*API).handleGetGitLFS},
	{"PUT", "/projects/{projectId}/settings/git/lfs", "git.settings", true, (*API).handlePutGitLFS},
	{"GET", "/projects/{projectId}/settings/deployment-protection", "project.read", true, (*API).handleGetProtection},
	{"PUT", "/projects/{projectId}/settings/deployment-protection", "project.settings", true, (*API).handlePutProtection},
	{"GET", "/projects/{projectId}/settings/oidc", "project.read", true, (*API).handleGetOIDC},
	{"PUT", "/projects/{projectId}/settings/oidc", "project.settings", true, (*API).handlePutOIDC},
	{"GET", "/projects/{projectId}/settings/functions", "git.settings", true, (*API).handleGetFunctions},
	{"PUT", "/projects/{projectId}/settings/functions", "git.settings", true, (*API).handlePutFunctions},
	{"GET", "/projects/{projectId}/settings/security", "project.read", true, (*API).handleGetSecurity},
	{"PUT", "/projects/{projectId}/settings/security", "project.settings", true, (*API).handlePutSecurity},
	{"GET", "/projects/{projectId}/settings/retention", "project.read", true, (*API).handleGetRetention},
	{"PUT", "/projects/{projectId}/settings/retention", "project.settings", true, (*API).handlePutRetention},
	{"GET", "/projects/{projectId}/settings/networking", "project.network", true, (*API).handleGetNetworking},
	{"PUT", "/projects/{projectId}/settings/networking", "project.network", true, (*API).handlePutNetworking},
	{"GET", "/projects/{projectId}/settings/advanced", "project.read", true, (*API).handleGetAdvanced},
	{"PUT", "/projects/{projectId}/settings/advanced", "project.settings", true, (*API).handlePutAdvanced},
	{"GET", "/projects/{projectId}/settings/passport", "project.read", true, (*API).handleGetPassport},
	{"PUT", "/projects/{projectId}/settings/passport", "project.settings", true, (*API).handlePutPassport},
	{"GET", "/projects/{projectId}/settings/microfrontends", "project.read", true, (*API).handleGetMicrofrontends},
	{"PUT", "/projects/{projectId}/settings/microfrontends", "project.settings", true, (*API).handlePutMicrofrontends},

	// environments (SRS §25)
	{"GET", "/projects/{projectId}/environments", "project.read", true, (*API).handleListEnvironments},
	{"POST", "/projects/{projectId}/environments", "project.settings", true, (*API).handleCreateEnvironment},
	{"GET", "/projects/{projectId}/environments/available", "project.read", true, (*API).handleEnvironmentsAvailable},
	{"GET", "/projects/{projectId}/environments/{envId}", "project.read", true, (*API).handleGetEnvironment},
	{"PATCH", "/projects/{projectId}/environments/{envId}", "project.settings", true, (*API).handlePatchEnvironment},
	{"DELETE", "/projects/{projectId}/environments/{envId}", "project.settings", true, (*API).handleDeleteEnvironment},
	{"POST", "/projects/{projectId}/environments/{envId}/branch", "project.settings", true, (*API).handleEnvBranch},
	{"POST", "/projects/{projectId}/environments/{envId}/domain", "project.settings", true, (*API).handleEnvDomain},
	{"GET", "/projects/{projectId}/environments/{envId}/range", "project.read", true, (*API).handleEnvRange},

	// hooks / crons / members / drains
	{"GET", "/projects/{projectId}/hooks", "project.read", true, (*API).handleListHooks},
	{"POST", "/projects/{projectId}/hooks", "hook.create", true, (*API).handleCreateHook},
	{"DELETE", "/projects/{projectId}/hooks/{hookId}", "hook.delete", true, (*API).handleDeleteHook},
	{"POST", "/projects/{projectId}/hooks/{hookId}/trigger", "hook.trigger", true, (*API).handleTriggerHook},
	{"GET", "/projects/{projectId}/crons", "cron.create", true, (*API).handleListCrons},
	{"POST", "/projects/{projectId}/crons", "cron.create", true, (*API).handleCreateCron},
	{"GET", "/projects/{projectId}/crons/history", "cron.update", true, (*API).handleCronHistory},
	{"GET", "/projects/{projectId}/crons/{cronId}", "cron.create", true, (*API).handleGetCron},
	{"PATCH", "/projects/{projectId}/crons/{cronId}", "cron.update", true, (*API).handlePatchCron},
	{"DELETE", "/projects/{projectId}/crons/{cronId}", "cron.delete", true, (*API).handleDeleteCron},
	{"POST", "/projects/{projectId}/crons/{cronId}/run", "cron.run", true, (*API).handleRunCron},
	{"GET", "/projects/{projectId}/members", "member.list", true, (*API).handleListProjectMembers},
	{"POST", "/projects/{projectId}/members", "member.invite", true, (*API).handleAddProjectMember},
	{"GET", "/projects/{projectId}/members/{username}", "member.list", true, (*API).handleGetProjectMember},
	{"PATCH", "/projects/{projectId}/members/{username}", "member.role", true, (*API).handlePatchProjectMember},
	{"DELETE", "/projects/{projectId}/members/{username}", "member.remove", true, (*API).handleRemoveProjectMember},
	{"POST", "/projects/{projectId}/members/invite", "member.invite", true, (*API).handleInviteMember},
	{"GET", "/projects/{projectId}/drains", "project.settings", true, (*API).handleListDrains},
	{"POST", "/projects/{projectId}/drains", "drain.create", true, (*API).handleCreateDrain},
	{"DELETE", "/projects/{projectId}/drains/{drainId}", "drain.delete", true, (*API).handleDeleteDrain},
	{"POST", "/projects/{projectId}/drains/{drainId}/test", "drain.create", true, (*API).handleTestDrain},

	// alerts + routing + silences (SRS §36)
	{"GET", "/projects/{projectId}/alerts", "project.read", true, (*API).handleListAlerts},
	{"POST", "/projects/{projectId}/alerts", "alert.create", true, (*API).handleCreateAlert},
	{"GET", "/projects/{projectId}/alerts/{alertId}", "project.read", true, (*API).handleGetAlert},
	{"PATCH", "/projects/{projectId}/alerts/{alertId}", "alert.update", true, (*API).handlePatchAlert},
	{"DELETE", "/projects/{projectId}/alerts/{alertId}", "alert.delete", true, (*API).handleDeleteAlert},
	{"POST", "/projects/{projectId}/alerts/{alertId}/silence", "alert.silence", true, (*API).handleSilenceAlert},
	{"POST", "/projects/{projectId}/alerts/{alertId}/unsilence", "alert.silence", true, (*API).handleUnsilenceAlert},
	{"GET", "/alerts/routing", "alertroute.list", true, (*API).handleGetAlertRouting},
	{"PUT", "/alerts/routing", "alertroute.update", true, (*API).handlePutAlertRouting},
	{"GET", "/alerts/silences", "silence.list", true, (*API).handleListSilences},
	{"POST", "/alerts/silences", "silence.create", true, (*API).handleCreateSilence},
	{"DELETE", "/alerts/silences/{id}", "silence.delete", true, (*API).handleDeleteSilence},

	// incidents (SRS §36)
	{"GET", "/projects/{projectId}/incidents", "project.read", true, (*API).handleListIncidents},
	{"POST", "/projects/{projectId}/incidents", "alert.create", true, (*API).handleCreateIncident},
	{"GET", "/incidents", "incident.list", true, (*API).handleListAllIncidents},
	{"GET", "/incidents/{id}", "incident.list", true, (*API).handleGetIncident},
	{"PATCH", "/incidents/{id}", "incident.update", true, (*API).handlePatchIncident},
	{"DELETE", "/incidents/{id}", "incident.delete", true, (*API).handleDeleteIncident},
	{"POST", "/incidents/{id}/acknowledge", "incident.update", true, (*API).handleAcknowledgeIncident},
	{"POST", "/incidents/{id}/resolve", "incident.update", true, (*API).handleResolveIncident},
	{"POST", "/incidents/{id}/postmortem", "incident.update", true, (*API).handleIncidentPostmortem},
	{"GET", "/incidents/{id}/timeline", "incident.list", true, (*API).handleIncidentTimeline},
	{"POST", "/incidents/{id}/timeline", "incident.update", true, (*API).handleAppendIncidentTimeline},

	// SLOs + synthetics (SRS §37)
	{"GET", "/slos", "slo.list", true, (*API).handleListSLOs},
	{"POST", "/slos", "slo.create", true, (*API).handleCreateSLO},
	{"GET", "/slos/{id}", "slo.list", true, (*API).handleGetSLO},
	{"PATCH", "/slos/{id}", "slo.update", true, (*API).handlePatchSLO},
	{"DELETE", "/slos/{id}", "slo.delete", true, (*API).handleDeleteSLO},
	{"GET", "/slos/{id}/budget", "slo.list", true, (*API).handleSLOBudget},
	{"GET", "/synthetic-checks", "synthetic.list", true, (*API).handleListSyntheticChecks},
	{"POST", "/synthetic-checks", "synthetic.create", true, (*API).handleCreateSyntheticCheck},
	{"GET", "/synthetic-checks/{id}", "synthetic.list", true, (*API).handleGetSyntheticCheck},
	{"PATCH", "/synthetic-checks/{id}", "synthetic.update", true, (*API).handlePatchSyntheticCheck},
	{"DELETE", "/synthetic-checks/{id}", "synthetic.delete", true, (*API).handleDeleteSyntheticCheck},
	{"POST", "/synthetic-checks/{id}/run", "synthetic.run", true, (*API).handleRunSyntheticCheck},
	{"GET", "/synthetic-checks/{id}/results", "synthetic.list", true, (*API).handleSyntheticResults},

	// firewall / cache / redirects
	{"GET", "/projects/{projectId}/firewall/rules", "project.read", true, (*API).handleListFirewallRules},
	{"POST", "/projects/{projectId}/firewall/rules", "firewall.create", true, (*API).handleCreateFirewallRule},
	{"GET", "/projects/{projectId}/firewall/rules/{ruleId}", "project.read", true, (*API).handleGetFirewallRule},
	{"DELETE", "/projects/{projectId}/firewall/rules/{ruleId}", "firewall.delete", true, (*API).handleDeleteFirewallRule},
	{"PATCH", "/projects/{projectId}/firewall/rules/{ruleId}", "firewall.update", true, (*API).handlePatchFirewallRule},
	{"GET", "/projects/{projectId}/firewall/events", "traffic.read", true, (*API).handleFirewallEvents},
	{"GET", "/projects/{projectId}/firewall/stats", "traffic.read", true, (*API).handleFirewallStats},
	{"POST", "/projects/{projectId}/firewall/whitelist", "firewall.create", true, (*API).handleFirewallWhitelist},
	{"GET", "/projects/{projectId}/cache/stats", "cache.stats", true, (*API).handleCacheStats},
	{"POST", "/projects/{projectId}/cache/purge", "cache.purge", true, (*API).handleCachePurge},
	{"POST", "/projects/{projectId}/cache/purge/path", "cache.purge", true, (*API).handleCachePurgePath},
	{"GET", "/projects/{projectId}/redirects", "project.read", true, (*API).handleListRedirects},
	{"POST", "/projects/{projectId}/redirects", "redirect.create", true, (*API).handleCreateRedirect},
	{"DELETE", "/projects/{projectId}/redirects/{redirectId}", "redirect.delete", true, (*API).handleDeleteRedirect},
	{"PUT", "/projects/{projectId}/redirects/bulk", "redirect.create", true, (*API).handleBulkRedirects},

	// analytics / observability (SRS §34)
	{"GET", "/projects/{projectId}/analytics/usage", "analytics.read", true, (*API).handleAnalyticsUsage},
	{"GET", "/projects/{projectId}/analytics/usage/timeseries", "analytics.read", true, (*API).handleAnalyticsTimeseries},
	{"GET", "/projects/{projectId}/analytics/paths", "analytics.read", true, (*API).handleAnalyticsPaths},
	{"GET", "/projects/{projectId}/analytics/status-codes", "analytics.read", true, (*API).handleAnalyticsStatusCodes},
	{"GET", "/projects/{projectId}/analytics/bandwidth", "analytics.read", true, (*API).handleAnalyticsBandwidth},
	{"GET", "/projects/{projectId}/analytics/requests", "analytics.read", true, (*API).handleAnalyticsRequests},
	{"GET", "/projects/{projectId}/analytics/invocations", "analytics.read", true, (*API).handleAnalyticsInvocations},
	{"GET", "/projects/{projectId}/observability/web-vitals", "webvital.read", true, (*API).handleWebVitals},
	{"POST", "/projects/{projectId}/observability/web-vitals/beacon", "webvital.read", true, (*API).handleWebVitalsBeacon},
	{"GET", "/projects/{projectId}/observability/web-vitals/timeseries", "webvital.read", true, (*API).handleWebVitalsTimeseries},
	{"GET", "/projects/{projectId}/observability/lcp", "webvital.read", true, (*API).handleAnalyticsUsage},
	{"GET", "/projects/{projectId}/observability/cls", "webvital.read", true, (*API).handleAnalyticsUsage},
	{"GET", "/projects/{projectId}/observability/fid", "webvital.read", true, (*API).handleAnalyticsUsage},
	{"GET", "/traces", "trace.read", true, (*API).handleListTraces},
	{"GET", "/traces/{id}", "trace.read", true, (*API).handleGetTrace},
	{"GET", "/traces/search", "trace.read", true, (*API).handleSearchTraces},
	{"GET", "/audit", "audit.read", true, (*API).handleListAudit},
	{"GET", "/audit/{id}", "audit.read", true, (*API).handleGetAudit},
	{"GET", "/audit/export", "audit.export", true, (*API).handleExportAudit},

	// volumes (SRS §31)
	{"GET", "/projects/{projectId}/volumes", "volume.read", true, (*API).handleListVolumes},
	{"POST", "/projects/{projectId}/volumes", "volume.create", true, (*API).handleCreateVolume},
	{"GET", "/projects/{projectId}/volumes/{volumeId}", "volume.read", true, (*API).handleGetVolume},
	{"DELETE", "/projects/{projectId}/volumes/{volumeId}", "volume.delete", true, (*API).handleDeleteVolume},
	{"POST", "/projects/{projectId}/volumes/{volumeId}/resize", "volume.resize", true, (*API).handleResizeVolume},
	{"GET", "/projects/{projectId}/volumes/{volumeId}/usage", "volume.read", true, (*API).handleVolumeUsage},
	{"POST", "/projects/{projectId}/volumes/{volumeId}/clone", "volume.create", true, (*API).handleCloneVolume},

	// storage classes (SRS §31)
	{"GET", "/storage-classes", "storageclass.list", true, (*API).handleListStorageClasses},
	{"POST", "/storage-classes", "storageclass.create", true, (*API).handleCreateStorageClass},
	{"GET", "/storage-classes/{id}", "storageclass.list", true, (*API).handleGetStorageClass},
	{"PATCH", "/storage-classes/{id}", "storageclass.update", true, (*API).handlePatchStorageClass},
	{"DELETE", "/storage-classes/{id}", "storageclass.delete", true, (*API).handleDeleteStorageClass},

	// object stores + buckets (SRS §32)
	{"GET", "/object-stores", "objectstore.list", true, (*API).handleListObjectStores},
	{"POST", "/object-stores", "objectstore.create", true, (*API).handleCreateObjectStore},
	{"GET", "/object-stores/{id}", "objectstore.list", true, (*API).handleGetObjectStore},
	{"PATCH", "/object-stores/{id}", "objectstore.update", true, (*API).handlePatchObjectStore},
	{"DELETE", "/object-stores/{id}", "objectstore.delete", true, (*API).handleDeleteObjectStore},
	{"POST", "/object-stores/{id}/validate", "objectstore.update", true, (*API).handleValidateObjectStore},
	{"GET", "/object-stores/{id}/buckets", "bucket.list", true, (*API).handleListBuckets},
	{"POST", "/object-stores/{id}/buckets", "bucket.create", true, (*API).handleCreateBucket},
	{"DELETE", "/object-stores/{id}/buckets/{bucket}", "bucket.delete", true, (*API).handleDeleteBucket},

	// backups / snapshots / policies (SRS §33)
	{"GET", "/projects/{projectId}/backups", "project.read", true, (*API).handleListBackups},
	{"GET", "/projects/{projectId}/backups/schedules", "project.read", true, (*API).handleListBackupSchedules},
	{"POST", "/projects/{projectId}/backups/schedules", "project.settings", true, (*API).handlePutBackupSchedule},
	{"GET", "/backups", "backup.list", true, (*API).handleListAllBackups},
	{"GET", "/backups/{id}", "backup.list", true, (*API).handleGetBackup},
	{"DELETE", "/backups/{id}", "backup.delete", true, (*API).handleDeleteBackup},
	{"POST", "/backups/{id}/restore", "backup.restore", true, (*API).handleRestoreBackup},
	{"POST", "/backups/{id}/verify-restore", "backup.restore", true, (*API).handleVerifyRestoreBackup},
	{"GET", "/backups/policies", "backuppolicy.list", true, (*API).handleListBackupPolicies},
	{"POST", "/backups/policies", "backuppolicy.create", true, (*API).handleCreateBackupPolicy},
	{"GET", "/backups/policies/{id}", "backuppolicy.list", true, (*API).handleGetBackupPolicy},
	{"PATCH", "/backups/policies/{id}", "backuppolicy.update", true, (*API).handlePatchBackupPolicy},
	{"DELETE", "/backups/policies/{id}", "backuppolicy.delete", true, (*API).handleDeleteBackupPolicy},
	{"GET", "/snapshots", "snapshot.list", true, (*API).handleListSnapshots},
	{"GET", "/snapshots/{id}", "snapshot.list", true, (*API).handleGetSnapshot},
	{"DELETE", "/snapshots/{id}", "snapshot.delete", true, (*API).handleDeleteSnapshot},
	{"GET", "/snapshots/{id}/history", "snapshot.list", true, (*API).handleSnapshotHistory},
	{"POST", "/snapshots/{id}/branch", "snapshot.create", true, (*API).handleBranchSnapshot},
	{"POST", "/snapshots/{id}/diff", "snapshot.list", true, (*API).handleSnapshotDiff},

	// project export/import/ssh/git/builds/services/networks
	{"POST", "/projects/{projectId}/export", "project.export", true, (*API).handleExportProject},
	{"POST", "/projects/{projectId}/import", "project.import", true, (*API).handleImportProject},
	{"PUT", "/projects/{projectId}/ssh", "ssh.toggle", true, (*API).handleSSHToggle},
	{"POST", "/projects/{projectId}/git/import", "git.import", true, (*API).handleGitImport},
	{"GET", "/projects/{projectId}/builds", "build.list", true, (*API).handleListBuilds},
	{"POST", "/projects/{projectId}/builds", "build.create", true, (*API).handleCreateBuild},
	{"POST", "/projects/{projectId}/builds/run", "build.create", true, (*API).handleCreateBuild},
	{"GET", "/projects/{projectId}/builds/{buildId}", "build.list", true, (*API).handleGetBuildByID},
	{"POST", "/projects/{projectId}/builds/{buildId}/cancel", "build.cancel", true, (*API).handleCancelBuild},
	{"GET", "/projects/{projectId}/builds/{buildId}/logs", "log.read", true, (*API).handleBuildLogs},
	{"GET", "/projects/{projectId}/builds/{buildId}/logs/stream", "log.read", true, (*API).handleBuildLogStream},
	{"GET", "/projects/{projectId}/git/branches", "git.import", true, (*API).handleGitBranches},
	{"GET", "/projects/{projectId}/rollouts", "deployment.list", true, (*API).handleListRollouts},
	{"GET", "/projects/{projectId}/services", "project.read", true, (*API).handleListServices},
	{"POST", "/projects/{projectId}/services", "project.deploy", true, (*API).handleProvisionService},
	{"GET", "/projects/{projectId}/services/{serviceName}", "project.read", true, (*API).handleGetService},
	{"POST", "/projects/{projectId}/services/{serviceName}/scale", "project.scale", true, (*API).handleScaleService},
	{"GET", "/projects/{projectId}/networks", "project.network", true, (*API).handleListNetworks},
	{"POST", "/projects/{projectId}/networks", "project.network", true, (*API).handleCreateNetwork},

	// config push + jobs
	{"POST", "/projects/{projectId}/config/push", "env.set", true, (*API).handlePushConfig},
	{"POST", "/jobs", "project.deploy", true, (*API).handleCreateJob},

	// ----------------------------------------------------------------------
	// global / usage / replicas / volumes (global-scope)
	// ----------------------------------------------------------------------
	{"GET", "/global/analytics", "analytics.read", true, (*API).handleGlobalAnalytics},
	{"GET", "/global/analytics/timeseries", "analytics.read", true, (*API).handleGlobalAnalyticsTimeseries},
	{"GET", "/usage", "analytics.read", true, (*API).handleUsage},
	{"GET", "/usage/bandwidth", "analytics.read", true, (*API).handleUsageBandwidth},
	{"GET", "/usage/requests", "analytics.read", true, (*API).handleUsageRequests},
	{"GET", "/usage/timeseries", "analytics.read", true, (*API).handleGlobalAnalyticsTimeseries},
	{"GET", "/usage/meters", "usage.read", true, (*API).handleListMeters},
	{"GET", "/usage/events", "usage.read", true, (*API).handleListUsageEvents},
	{"GET", "/replicas", "replica.list", true, (*API).handleGlobalReplicas},
	{"GET", "/replicas/{replicaId}", "replica.list", true, (*API).handleGlobalReplica},
	{"GET", "/volumes", "volume.read", true, (*API).handleListVolumes},
	{"POST", "/volumes", "volume.create", true, (*API).handleCreateVolume},
	{"GET", "/volumes/{volumeId}", "volume.read", true, (*API).handleGetVolume},
	{"DELETE", "/volumes/{volumeId}", "volume.delete", true, (*API).handleDeleteVolume},
	{"POST", "/volumes/{volumeId}/resize", "volume.resize", true, (*API).handleResizeVolume},
	{"GET", "/volumes/{volumeId}/usage", "volume.read", true, (*API).handleVolumeUsage},
	{"GET", "/guest-bases", "project.read", true, (*API).handleListGuestBases},
	{"GET", "/overview", "project.read", true, (*API).handleOverview},

	// ----------------------------------------------------------------------
	// images + registry + vCenter conversion (SRS §21)
	// ----------------------------------------------------------------------
	{"GET", "/images", "project.read", true, (*API).handleListImages},
	{"GET", "/images/base", "project.read", true, (*API).handleBaseImage},
	{"GET", "/images/base/readiness", "project.read", true, (*API).handleBaseImageReadiness},
	{"POST", "/images/custom", "image.upload", true, (*API).handleUploadCustomImage},
	{"POST", "/images/custom/oci", "image.upload", true, (*API).handleUploadCustomOCIImage},
	{"POST", "/images/upload/vmdk", "image.upload", true, (*API).handleUploadVMDK},
	{"POST", "/images/upload/qcow2", "image.upload", true, (*API).handleUploadQCOW2},
	{"POST", "/images/upload/ova", "image.upload", true, (*API).handleUploadOVA},
	{"POST", "/images/upload/raw", "image.upload", true, (*API).handleUploadRaw},
	{"POST", "/images/vcenter/import", "image.upload", true, (*API).handleImportFromVCenter},
	{"POST", "/images/opennebula/import", "image.upload", true, (*API).handleImportFromOpenNebula},
	{"POST", "/images/proxmox/import", "image.upload", true, (*API).handleImportFromProxmox},
	{"GET", "/images/search", "project.read", true, (*API).handleImageSearch},
	{"GET", "/images/stats", "project.read", true, (*API).handleImageStats},
	{"GET", "/images/ml", "project.read", true, (*API).handleImageSearch},
	{"POST", "/images/prune", "cache.purge", true, (*API).handlePruneImages},
	{"GET", "/images/{reference}", "project.read", true, (*API).handleGetImage},
	{"DELETE", "/images/{reference}", "project.delete", true, (*API).handleDeleteImage},
	{"GET", "/images/{reference}/lineage", "project.read", true, (*API).handleImageLineage},
	{"POST", "/images/{reference}/sign", "image.sign", true, (*API).handleSignImage},
	{"GET", "/images/{reference}/sbom", "image.sbom", true, (*API).handleImageSBOM},
	{"GET", "/images/{reference}/provenance", "image.sbom", true, (*API).handleImageProvenance},
	{"POST", "/images/{reference}/scan", "image.scan", true, (*API).handleScanImage},
	// Pulling materialises an image listed in the upstream catalog into the
	// local cache. Porter ships no guest images, so this is how one arrives.
	{"POST", "/images/{reference}/pull", "image.upload", true, (*API).handlePullImage},

	{"GET", "/registries", "registry.list", true, (*API).handleListRegistries},
	{"POST", "/registries", "registry.create", true, (*API).handleCreateRegistry},
	{"GET", "/registries/{id}", "registry.list", true, (*API).handleGetRegistry},
	{"PATCH", "/registries/{id}", "registry.update", true, (*API).handlePatchRegistry},
	{"DELETE", "/registries/{id}", "registry.delete", true, (*API).handleDeleteRegistry},
	{"POST", "/registries/{id}/validate", "registry.update", true, (*API).handleValidateRegistry},

	// ----------------------------------------------------------------------
	// VMs (compat tree, SRS §12)
	// ----------------------------------------------------------------------
	{"GET", "/vms", "replica.list", true, (*API).handleListAllVMs},
	{"GET", "/vms/{replicaId}", "replica.list", true, (*API).handleGetVMCompat},
	{"POST", "/vms/{replicaId}/start", "replica.start", true, (*API).handleReplicaStartByID},
	{"POST", "/vms/{replicaId}/stop", "replica.stop", true, (*API).handleReplicaStopByID},
	{"POST", "/vms/{replicaId}/restart", "replica.restart", true, (*API).handleReplicaRestartByID},
	{"POST", "/vms/{replicaId}/pause", "replica.pause", true, (*API).handleReplicaPauseByID},
	{"POST", "/vms/{replicaId}/resume", "replica.resume", true, (*API).handleReplicaResumeByID},
	{"POST", "/vms/{replicaId}/reboot", "replica.reboot", true, (*API).handleReplicaRebootByID},
	{"POST", "/vms/{replicaId}/snapshot", "replica.snapshot", true, (*API).handleReplicaSnapshotByID},
	{"POST", "/vms/{replicaId}/restore", "replica.restore", true, (*API).handleReplicaRestoreByID},
	{"POST", "/vms/{replicaId}/recover", "replica.restore", true, (*API).handleReplicaRestoreByID},
	{"POST", "/vms/{replicaId}/migrate", "vm.migrate", true, (*API).handleMigrateVM},
	{"DELETE", "/vms/{replicaId}", "replica.delete", true, (*API).handleVMCompatDelete},
	{"GET", "/vms/{replicaId}/domains", "domain.list", true, (*API).handleVMCompatDomains},
	{"GET", "/vms/{replicaId}/logs", "log.read", true, (*API).handleReplicaLogsByID},
	{"GET", "/vms/{replicaId}/logs/stream", "log.read", true, (*API).handleReplicaLogStream},
	{"GET", "/vms/{replicaId}/metrics", "metric.read", true, (*API).handleReplicaMetricsByID},
	{"GET", "/vms/{replicaId}/traffic", "traffic.read", true, (*API).handleReplicaTrafficByID},
	{"GET", "/vms/{replicaId}/health", "replica.list", true, (*API).handleReplicaHealthByID},
	{"GET", "/vms/{replicaId}/ssh-info", "ssh.connect", true, (*API).handleSSHInfoByID},
	{"POST", "/vms/{replicaId}/ssh-cert", "ssh.connect", true, (*API).handleSSHCertByID},
	{"POST", "/vms/{replicaId}/exec", "replica.exec", true, (*API).handleReplicaExecByID},
	{"GET", "/vms/{replicaId}/console", "console.open", true, (*API).handleReplicaConsoleByID},
	{"POST", "/vms/{replicaId}/console-token", "console.open", true, (*API).handleMintConsoleToken},
	{"GET", "/vms/{replicaId}/placement", "replica.list", true, (*API).handleGetPlacement},
	{"GET", "/vms/{replicaId}/snapshots", "replica.list", true, (*API).handleListSnapshots},
	{"GET", "/vms/{replicaId}/aux", "replica.list", true, (*API).handleListAuxVMs},
	{"POST", "/vms/{replicaId}/aux", "replica.start", true, (*API).handleProvisionAuxVM},
	{"DELETE", "/vms/{replicaId}/aux/{auxId}", "replica.delete", true, (*API).handleDeleteAuxVM},
	{"GET", "/vms/{replicaId}/aux/{auxId}/cdp", "console.open", true, (*API).handleAuxCDP},

	// host / logs / traffic
	{"GET", "/host/overview", "metric.read", true, (*API).handleHostOverview},
	{"GET", "/host/ports", "metric.read", true, (*API).handleHostPorts},
	{"GET", "/host/kernel", "metric.read", true, (*API).handleHostKernel},
	{"GET", "/host/prerequisites", "metric.read", true, (*API).handleHostPrerequisites},
	{"GET", "/host/runtime", "metric.read", true, (*API).handleRuntimeConfig},
	{"GET", "/logs", "log.read", true, (*API).handleDaemonLogs},
	{"GET", "/traffic", "traffic.read", true, (*API).handleAllTraffic},
	{"DELETE", "/traffic", "cache.purge", true, (*API).handleClearTraffic},
	{"GET", "/traffic/search", "traffic.read", true, (*API).handleTrafficSearch},

	// ----------------------------------------------------------------------
	// nodes / providers / regions / zones / node pools (SRS §14, §15, §17)
	// ----------------------------------------------------------------------
	{"GET", "/servers", "server.register", true, (*API).handleListServers},
	{"POST", "/servers", "server.register", true, (*API).handleRegisterServer},
	{"GET", "/servers/{id}", "server.register", true, (*API).handleGetServer},
	{"POST", "/servers/{id}/heartbeat", "", false, (*API).handleServerHeartbeat},
	{"GET", "/servers/{id}/ssh", "server.register", true, (*API).handleServerSSH},
	{"DELETE", "/servers/{id}", "server.remove", true, (*API).handleDeleteServer},
	{"GET", "/servers/{id}/detail", "server.register", true, (*API).handleServerDetail},
	{"GET", "/servers/{id}/analytics", "server.register", true, (*API).handleServerAnalytics},
	{"GET", "/servers/{id}/vms", "server.register", true, (*API).handleServerVMs},
	{"GET", "/servers/{id}/logs", "server.register", true, (*API).handleServerLogs},
	{"POST", "/servers/{id}/cordon", "node.cordon", true, (*API).handleServerCordon},
	{"POST", "/servers/{id}/uncordon", "node.cordon", true, (*API).handleServerUncordon},
	{"POST", "/servers/{id}/drain", "node.drain", true, (*API).handleServerDrain},
	{"POST", "/servers/{id}/maintenance", "node.maintenance", true, (*API).handleServerMaintenance},
	{"POST", "/servers/{id}/upgrade", "node.upgrade", true, (*API).handleServerUpgrade},
	{"POST", "/servers/{id}/recover", "node.recover", true, (*API).handleServerRecover},
	{"POST", "/servers/{id}/replace", "node.replace", true, (*API).handleServerReplace},
	{"POST", "/servers/import", "server.register", true, (*API).handleImportServer},

	{"POST", "/nodes/enroll", "", false, (*API).handleNodeEnroll},
	{"GET", "/nodes/enrollment-tokens", "server.register", true, (*API).handleListEnrollmentTokens},
	{"POST", "/nodes/enrollment-tokens", "project.settings", true, (*API).handleCreateEnrollmentToken},
	{"DELETE", "/nodes/enrollment-tokens/{id}", "server.register", true, (*API).handleRevokeEnrollmentToken},

	{"GET", "/providers", "provider.list", true, (*API).handleListProviders},
	{"POST", "/providers", "provider.create", true, (*API).handleCreateProvider},
	{"GET", "/providers/{id}", "provider.list", true, (*API).handleGetProvider},
	{"PATCH", "/providers/{id}", "provider.update", true, (*API).handlePatchProvider},
	{"DELETE", "/providers/{id}", "provider.delete", true, (*API).handleDeleteProvider},

	{"GET", "/regions", "region.list", true, (*API).handleListRegions},
	{"POST", "/regions", "region.create", true, (*API).handleCreateRegion},
	{"GET", "/regions/{id}", "region.list", true, (*API).handleGetRegion},
	{"PATCH", "/regions/{id}", "region.update", true, (*API).handlePatchRegion},
	{"DELETE", "/regions/{id}", "region.delete", true, (*API).handleDeleteRegion},

	{"GET", "/zones", "zone.list", true, (*API).handleListZones},
	{"POST", "/zones", "zone.create", true, (*API).handleCreateZone},
	{"GET", "/zones/{id}", "zone.list", true, (*API).handleGetZone},
	{"PATCH", "/zones/{id}", "zone.update", true, (*API).handlePatchZone},
	{"DELETE", "/zones/{id}", "zone.delete", true, (*API).handleDeleteZone},

	{"GET", "/node-pools", "nodepool.list", true, (*API).handleListNodePools},
	{"POST", "/node-pools", "nodepool.create", true, (*API).handleCreateNodePool},
	{"GET", "/node-pools/{id}", "nodepool.list", true, (*API).handleGetNodePool},
	{"PATCH", "/node-pools/{id}", "nodepool.update", true, (*API).handlePatchNodePool},
	{"DELETE", "/node-pools/{id}", "nodepool.delete", true, (*API).handleDeleteNodePool},
	{"GET", "/node-pools/{id}/nodes", "nodepool.list", true, (*API).handleListNodePoolNodes},
	{"POST", "/node-pools/{id}/nodes/{nodeId}", "nodepool.update", true, (*API).handleAddNodePoolNode},
	{"DELETE", "/node-pools/{id}/nodes/{nodeId}", "nodepool.update", true, (*API).handleRemoveNodePoolNode},

	{"GET", "/capacity/policies", "project.read", true, (*API).handleListCapacityPolicies},
	{"PUT", "/capacity/policies", "project.settings", true, (*API).handlePutCapacityPolicy},
	{"GET", "/capacity", "nodepool.list", true, (*API).handleGetCapacity},
	{"GET", "/scheduler/placements", "replica.list", true, (*API).handleListPlacements},
	{"GET", "/scheduler/decisions", "replica.list", true, (*API).handleListSchedulingDecisions},

	// ----------------------------------------------------------------------
	// billing / commerce (SRS §13, §46–§49)
	// ----------------------------------------------------------------------
	{"GET", "/billing/plans", "billing.read", true, (*API).handleListPlans},
	{"POST", "/billing/plans", "billing.manage", true, (*API).handlePutPlan},
	{"GET", "/products", "product.list", true, (*API).handleListProducts},
	{"POST", "/products", "product.create", true, (*API).handleCreateProduct},
	{"GET", "/products/{id}", "product.list", true, (*API).handleGetProduct},
	{"PATCH", "/products/{id}", "product.update", true, (*API).handlePatchProduct},
	{"DELETE", "/products/{id}", "product.delete", true, (*API).handleDeleteProduct},
	{"GET", "/prices", "price.list", true, (*API).handleListPrices},
	{"POST", "/prices", "price.create", true, (*API).handleCreatePrice},
	{"PATCH", "/prices/{id}", "price.update", true, (*API).handlePatchPrice},
	{"DELETE", "/prices/{id}", "price.delete", true, (*API).handleDeletePrice},
	{"GET", "/entitlements", "entitlement.list", true, (*API).handleListEntitlements},
	{"POST", "/entitlements", "entitlement.create", true, (*API).handleCreateEntitlement},
	{"DELETE", "/entitlements/{id}", "entitlement.delete", true, (*API).handleDeleteEntitlement},
	{"GET", "/projects/{projectId}/billing/subscription", "billing.read", true, (*API).handleGetSubscription},
	{"POST", "/projects/{projectId}/billing/subscription", "billing.manage", true, (*API).handleSubscribe},
	{"PATCH", "/projects/{projectId}/billing/subscription", "billing.manage", true, (*API).handlePatchSubscription},
	{"DELETE", "/projects/{projectId}/billing/subscription", "billing.manage", true, (*API).handleCancelSubscription},
	{"GET", "/subscriptions", "subscription.list", true, (*API).handleListSubscriptions},
	{"GET", "/subscriptions/{id}", "subscription.list", true, (*API).handleGetSubscriptionByID},
	{"POST", "/subscriptions/{id}/suspend", "subscription.manage", true, (*API).handleSuspendSubscription},
	{"POST", "/subscriptions/{id}/resume", "subscription.manage", true, (*API).handleResumeSubscription},
	{"GET", "/projects/{projectId}/billing/invoice", "billing.read", true, (*API).handleInvoicePreview},
	{"GET", "/invoices", "invoice.list", true, (*API).handleListInvoices},
	{"GET", "/invoices/{id}", "invoice.list", true, (*API).handleGetInvoice},
	{"POST", "/invoices/{id}/finalize", "invoice.manage", true, (*API).handleFinalizeInvoice},
	{"POST", "/invoices/{id}/pay", "payment.manage", true, (*API).handlePayInvoice},
	{"GET", "/invoices/{id}/lines", "invoice.list", true, (*API).handleListInvoiceLines},
	{"GET", "/payments", "payment.list", true, (*API).handleListPayments},
	{"GET", "/payments/{id}", "payment.list", true, (*API).handleGetPayment},
	{"POST", "/payments/{id}/refund", "payment.refund", true, (*API).handleRefundPayment},
	{"GET", "/payment-methods", "payment.list", true, (*API).handleListPaymentMethods},
	{"POST", "/payment-methods", "payment.manage", true, (*API).handleCreatePaymentMethod},
	{"DELETE", "/payment-methods/{id}", "payment.manage", true, (*API).handleDeletePaymentMethod},
	{"GET", "/credits", "credit.list", true, (*API).handleListCredits},
	{"POST", "/credits", "credit.create", true, (*API).handleCreateCredit},
	{"GET", "/refunds", "refund.list", true, (*API).handleListRefunds},
	{"GET", "/tax-rates", "tax.list", true, (*API).handleListTaxRates},
	{"POST", "/tax-rates", "tax.manage", true, (*API).handleCreateTaxRate},
	{"GET", "/dunning/policies", "dunning.list", true, (*API).handleListDunningPolicies},
	{"POST", "/dunning/policies", "dunning.manage", true, (*API).handleCreateDunningPolicy},
	{"GET", "/dunning/runs", "dunning.list", true, (*API).handleListDunningRuns},

	// ----------------------------------------------------------------------
	// workflows / webhooks / tasks (SRS §54)
	// ----------------------------------------------------------------------
	{"GET", "/workflows", "workflow.list", true, (*API).handleListWorkflows},
	{"POST", "/workflows", "workflow.create", true, (*API).handleCreateWorkflow},
	{"GET", "/workflows/{id}", "workflow.list", true, (*API).handleGetWorkflow},
	{"PATCH", "/workflows/{id}", "workflow.update", true, (*API).handlePatchWorkflow},
	{"DELETE", "/workflows/{id}", "workflow.delete", true, (*API).handleDeleteWorkflow},
	{"POST", "/workflows/{id}/run", "workflow.run", true, (*API).handleRunWorkflow},
	{"GET", "/workflows/{id}/runs", "workflow.list", true, (*API).handleListWorkflowRuns},
	{"GET", "/workflow-runs/{id}", "workflow.list", true, (*API).handleGetWorkflowRun},
	{"POST", "/workflow-runs/{id}/cancel", "workflow.run", true, (*API).handleCancelWorkflowRun},
	{"GET", "/operations", "event.read", true, (*API).handleListOperations},
	{"GET", "/operations/{id}", "event.read", true, (*API).handleGetOperation},
	{"POST", "/operations/{id}/cancel", "event.read", true, (*API).handleCancelOperation},
	{"GET", "/webhooks", "webhook.list", true, (*API).handleListWebhooks},
	{"POST", "/webhooks", "webhook.create", true, (*API).handleCreateWebhook},
	{"GET", "/webhooks/{id}", "webhook.list", true, (*API).handleGetWebhook},
	{"PATCH", "/webhooks/{id}", "webhook.update", true, (*API).handlePatchWebhook},
	{"DELETE", "/webhooks/{id}", "webhook.delete", true, (*API).handleDeleteWebhook},
	{"POST", "/webhooks/{id}/test", "webhook.update", true, (*API).handleTestWebhook},
	{"GET", "/maintenance-windows", "maintenance.list", true, (*API).handleListMaintenanceWindows},
	{"POST", "/maintenance-windows", "maintenance.create", true, (*API).handleCreateMaintenanceWindow},
	{"GET", "/maintenance-windows/{id}", "maintenance.list", true, (*API).handleGetMaintenanceWindow},
	{"PATCH", "/maintenance-windows/{id}", "maintenance.update", true, (*API).handlePatchMaintenanceWindow},
	{"DELETE", "/maintenance-windows/{id}", "maintenance.delete", true, (*API).handleDeleteMaintenanceWindow},

	// ----------------------------------------------------------------------
	// AI + MCP + impersonation (SRS §44)
	// ----------------------------------------------------------------------
	{"GET", "/ai/agents", "aiagent.list", true, (*API).handleListAIAgents},
	{"POST", "/ai/agents", "aiagent.create", true, (*API).handleCreateAIAgent},
	{"GET", "/ai/agents/{id}", "aiagent.list", true, (*API).handleGetAIAgent},
	{"PATCH", "/ai/agents/{id}", "aiagent.update", true, (*API).handlePatchAIAgent},
	{"DELETE", "/ai/agents/{id}", "aiagent.delete", true, (*API).handleDeleteAIAgent},
	{"POST", "/ai/agents/{id}/plans", "aiplan.create", true, (*API).handleCreateAIPlan},
	{"GET", "/ai/plans/{id}", "aiplan.list", true, (*API).handleGetAIPlan},
	{"POST", "/ai/plans/{id}/approve", "aiplan.approve", true, (*API).handleApproveAIPlan},
	{"POST", "/ai/plans/{id}/reject", "aiplan.approve", true, (*API).handleRejectAIPlan},
	{"POST", "/mcp/search", "mcp.search", true, (*API).handleMCPSearch},
	{"POST", "/mcp/describe", "mcp.describe", true, (*API).handleMCPDescribe},
	{"POST", "/mcp/call", "mcp.call", true, (*API).handleMCPCall},
	{"POST", "/support/impersonate", "support.impersonate", true, (*API).handleImpersonate},
	{"POST", "/support/impersonate/end", "support.impersonate", true, (*API).handleEndImpersonate},

	// ----------------------------------------------------------------------
	// extensions + marketplace (SRS §45)
	// ----------------------------------------------------------------------
	{"GET", "/extensions", "extension.list", true, (*API).handleListExtensions},
	{"POST", "/extensions", "extension.create", true, (*API).handleInstallExtension},
	{"GET", "/extensions/{id}", "extension.list", true, (*API).handleGetExtension},
	{"PATCH", "/extensions/{id}", "extension.update", true, (*API).handlePatchExtension},
	{"DELETE", "/extensions/{id}", "extension.delete", true, (*API).handleUninstallExtension},
	{"GET", "/marketplace/items", "marketplace.list", true, (*API).handleListMarketplaceItems},
	{"GET", "/marketplace/items/{id}", "marketplace.list", true, (*API).handleGetMarketplaceItem},
	{"POST", "/marketplace/items/{id}/install", "marketplace.install", true, (*API).handleInstallMarketplaceItem},

	// ----------------------------------------------------------------------
	// templates + shares + email + cloudflare
	// ----------------------------------------------------------------------
	{"GET", "/templates/services", "template.read", true, (*API).handleListServiceTemplates},
	{"POST", "/templates/services", "template.manage", true, (*API).handlePutServiceTemplate},
	{"DELETE", "/templates/services/{name}", "template.manage", true, (*API).handleDeleteServiceTemplate},
	{"POST", "/projects/{projectId}/shares", "share.create", true, (*API).handleShareOpen},
	{"GET", "/projects/{projectId}/shares", "project.read", true, (*API).handleShareList},
	{"DELETE", "/projects/{projectId}/shares/{shareId}", "share.delete", true, (*API).handleShareClose},
	{"POST", "/projects/{projectId}/email/identities", "email.manage", true, (*API).handleEmailIdentity},
	{"POST", "/projects/{projectId}/email/send", "email.send", true, (*API).handleEmailSend},
	{"POST", "/projects/{projectId}/cf/tunnels", "cf.tunnel", true, (*API).handleCFTunnelCreate},
	{"DELETE", "/projects/{projectId}/cf/tunnels/{tunnelId}", "cf.tunnel", true, (*API).handleCFTunnelDelete},
	{"POST", "/projects/{projectId}/cf/tunnels/{tunnelId}/ingress", "cf.tunnel", true, (*API).handleCFIngress},
	{"POST", "/projects/{projectId}/cf/dns", "cf.dns", true, (*API).handleCFDNS},
	{"POST", "/projects/{projectId}/cf/dns/a", "cf.dns", true, (*API).handleCFDNSA},
	{"POST", "/projects/{projectId}/cf/dns/txt", "cf.dns", true, (*API).handleCFDNSTXT},
	{"DELETE", "/projects/{projectId}/cf/dns/{zoneId}/{recordId}", "cf.dns", true, (*API).handleCFDNSDelete},
	{"GET", "/projects/{projectId}/cf/zones", "cf.dns", true, (*API).handleCFZones},

	// ----------------------------------------------------------------------
	// stacks + VPS + kernel + bench + telemetry
	// ----------------------------------------------------------------------
	{"GET", "/stacks", "project.read", true, (*API).handleListStacks},
	{"GET", "/stacks/{stackId}", "project.read", true, (*API).handleGetStack},
	{"DELETE", "/stacks/{stackId}", "project.delete", true, (*API).handleDeleteStack},
	{"POST", "/projects/{projectId}/vps", "project.settings", true, (*API).handleEnableVPS},
	{"GET", "/projects/{projectId}/vps", "project.read", true, (*API).handleGetVPS},
	{"DELETE", "/projects/{projectId}/vps", "project.settings", true, (*API).handleDisableVPS},
	{"POST", "/system/kernel/build", "kernel.build", true, (*API).handleKernelBuild},
	{"GET", "/bench/runs", "metric.read", true, (*API).handleListBenchRuns},
	{"POST", "/bench/runs", "project.settings", true, (*API).handleRecordBenchRun},

	// ----------------------------------------------------------------------
	// gitops / webhooks
	// ----------------------------------------------------------------------
	{"POST", "/hooks/github", "", false, (*API).handleGitHubWebhook},
	{"POST", "/hooks/gitlab", "", false, (*API).handleGitLabWebhook},
	{"POST", "/hooks/gitea", "", false, (*API).handleGiteaWebhook},
	{"POST", "/hooks/generic", "", false, (*API).handleGenericWebhook},

	// ----------------------------------------------------------------------
	// K8s (EXTENSION — SRS §4 non-goal is "K8s distribution"; this hosts K8s
	// clusters as workloads + adopts existing clusters. Never exposes a
	// Kubernetes API server. Documented in the SRS addendum, not §40.)
	// ----------------------------------------------------------------------
	{"GET", "/k8s/clusters", "k8s.cluster.list", true, (*API).handleListK8sClusters},
	{"POST", "/k8s/clusters", "k8s.cluster.create", true, (*API).handleCreateK8sCluster},
	{"GET", "/k8s/clusters/{id}", "k8s.cluster.list", true, (*API).handleGetK8sCluster},
	{"DELETE", "/k8s/clusters/{id}", "k8s.cluster.delete", true, (*API).handleDeleteK8sCluster},
	{"POST", "/k8s/clusters/{id}/scale", "k8s.cluster.update", true, (*API).handleScaleK8sCluster},
	{"POST", "/k8s/clusters/{id}/upgrade", "k8s.cluster.update", true, (*API).handleUpgradeK8sCluster},
	{"GET", "/k8s/clusters/{id}/kubeconfig", "k8s.cluster.read", true, (*API).handleK8sKubeconfig},
	{"GET", "/k8s/clusters/{id}/nodes", "k8s.cluster.list", true, (*API).handleListK8sNodes},
	{"POST", "/k8s/clusters/{id}/nodes/{nodeId}/cordon", "k8s.cluster.update", true, (*API).handleK8sCordonNode},
	{"POST", "/k8s/clusters/{id}/nodes/{nodeId}/uncordon", "k8s.cluster.update", true, (*API).handleK8sUncordonNode},
	{"POST", "/k8s/clusters/{id}/nodes/{nodeId}/drain", "k8s.cluster.update", true, (*API).handleK8sDrainNode},
	{"GET", "/k8s/clusters/{id}/workloads", "k8s.cluster.list", true, (*API).handleListK8sWorkloads},
	{"GET", "/k8s/clusters/{id}/events", "k8s.cluster.list", true, (*API).handleListK8sEvents},
	{"GET", "/k8s/clusters/{id}/logs/{namespace}/{pod}", "k8s.cluster.read", true, (*API).handleK8sPodLogs},
	{"POST", "/k8s/clusters/{id}/exec/{namespace}/{pod}", "k8s.cluster.exec", true, (*API).handleK8sPodExec},
	{"POST", "/k8s/adopt", "k8s.cluster.adopt", true, (*API).handleAdoptK8sCluster},
	{"POST", "/k8s/clusters/{id}/release", "k8s.cluster.adopt", true, (*API).handleReleaseK8sCluster},
	{"POST", "/k8s/agents/enroll", "", false, (*API).handleK8sAgentEnroll},
	{"GET", "/k8s/agents", "k8s.agent.list", true, (*API).handleListK8sAgents},
	{"GET", "/k8s/agents/{id}", "k8s.agent.list", true, (*API).handleGetK8sAgent},
	{"DELETE", "/k8s/agents/{id}", "k8s.agent.delete", true, (*API).handleDeleteK8sAgent},
	{"GET", "/k8s/clusters/{id}/agent-status", "k8s.agent.list", true, (*API).handleK8sAgentStatus},

	// ----------------------------------------------------------------------
	// Operator admin surface (server sub-pages, credentials, destinations,
	// storages, notifications, shared variables, instance settings, profile,
	// terminal, uploads/downloads). Adopted onto Porter semantics; not a
	// second API shape.
	// ----------------------------------------------------------------------
	{"GET", "/servers/{id}/advanced", "server.register", true, (*API).handleServerAdvanced},
	{"GET", "/servers/{id}/proxy", "server.register", true, (*API).handleServerProxy},
	{"GET", "/servers/{id}/proxy/dynamic-confs", "server.register", true, (*API).handleServerProxyDynamicConfs},
	{"GET", "/servers/{id}/proxy/logs", "server.register", true, (*API).handleServerProxyLogs},
	{"GET", "/servers/{id}/cloudflare-tunnel", "server.register", true, (*API).handleServerCloudflareTunnel},
	{"GET", "/servers/{id}/log-drains", "server.register", true, (*API).handleServerLogDrains},
	{"POST", "/servers/{id}/log-drains", "server.register", true, (*API).handleUpdateServerLogDrains},
	{"GET", "/servers/{id}/docker-cleanup", "server.register", true, (*API).handleServerDockerCleanup},
	{"POST", "/servers/{id}/docker-cleanup/run", "server.register", true, (*API).handleRunServerDockerCleanup},
	{"GET", "/servers/{id}/sentinel", "server.register", true, (*API).handleServerSentinel},
	{"GET", "/servers/{id}/sentinel/logs", "server.register", true, (*API).handleServerSentinelLogs},
	{"GET", "/servers/{id}/security/patches", "server.register", true, (*API).handleServerSecurityPatches},
	{"GET", "/servers/{id}/security/terminal-access", "server.register", true, (*API).handleServerTerminalAccess},
	{"GET", "/servers/{id}/ca-certificate", "server.register", true, (*API).handleServerCACertificate},
	{"GET", "/servers/{id}/private-key", "server.register", true, (*API).handleServerPrivateKey},
	{"GET", "/servers/{id}/cloud-provider-token", "server.register", true, (*API).handleServerCloudProviderToken},
	{"GET", "/servers/{id}/destinations", "server.register", true, (*API).handleServerDestinations},
	{"POST", "/servers/{id}/validate", "server.register", true, (*API).handleValidateServer},

	{"GET", "/security/private-keys", "security.privatekey.list", true, (*API).handleListPrivateKeys},
	{"POST", "/security/private-keys", "security.privatekey.create", true, (*API).handleCreatePrivateKey},
	{"GET", "/security/private-keys/{id}", "security.privatekey.list", true, (*API).handleGetPrivateKey},
	{"DELETE", "/security/private-keys/{id}", "security.privatekey.delete", true, (*API).handleDeletePrivateKey},
	{"GET", "/security/cloud-tokens", "security.cloudtoken.list", true, (*API).handleListCloudTokens},
	{"POST", "/security/cloud-tokens", "security.cloudtoken.create", true, (*API).handleCreateCloudToken},
	{"GET", "/security/cloud-tokens/{id}", "security.cloudtoken.list", true, (*API).handleGetCloudToken},
	{"PATCH", "/security/cloud-tokens/{id}", "security.cloudtoken.update", true, (*API).handlePatchCloudToken},
	{"DELETE", "/security/cloud-tokens/{id}", "security.cloudtoken.delete", true, (*API).handleDeleteCloudToken},
	{"POST", "/security/cloud-tokens/{id}/validate", "security.cloudtoken.update", true, (*API).handleValidateCloudToken},
	{"GET", "/security/integration-tokens", "security.integration.list", true, (*API).handleListIntegrationTokens},
	{"POST", "/security/integration-tokens", "security.integration.create", true, (*API).handleCreateIntegrationToken},
	{"DELETE", "/security/integration-tokens/{id}", "security.integration.delete", true, (*API).handleDeleteIntegrationToken},
	{"GET", "/security/cloud-init-scripts", "security.cloudinit.list", true, (*API).handleListCloudInitScripts},
	{"POST", "/security/cloud-init-scripts", "security.cloudinit.create", true, (*API).handleCreateCloudInitScript},
	{"GET", "/security/cloud-init-scripts/{id}", "security.cloudinit.list", true, (*API).handleGetCloudInitScript},
	{"PATCH", "/security/cloud-init-scripts/{id}", "security.cloudinit.update", true, (*API).handlePatchCloudInitScript},
	{"DELETE", "/security/cloud-init-scripts/{id}", "security.cloudinit.delete", true, (*API).handleDeleteCloudInitScript},
	{"GET", "/security/api-tokens", "security.apitoken.list", true, (*API).handleListAPITokens},
	{"POST", "/security/api-tokens", "security.apitoken.create", true, (*API).handleCreateAPIToken},
	{"DELETE", "/security/api-tokens/{id}", "security.apitoken.delete", true, (*API).handleDeleteAPIToken},

	{"GET", "/destinations", "destination.list", true, (*API).handleListDestinations},
	{"POST", "/destinations", "destination.create", true, (*API).handleCreateDestination},
	{"GET", "/destinations/{id}", "destination.list", true, (*API).handleGetDestination},
	{"PATCH", "/destinations/{id}", "destination.update", true, (*API).handlePatchDestination},
	{"DELETE", "/destinations/{id}", "destination.delete", true, (*API).handleDeleteDestination},
	{"GET", "/destinations/{id}/resources", "destination.list", true, (*API).handleDestinationResources},

	{"GET", "/storages", "storage.list", true, (*API).handleListStorages},
	{"POST", "/storages", "storage.create", true, (*API).handleCreateStorage},
	{"GET", "/storages/{id}", "storage.list", true, (*API).handleGetStorage},
	{"PATCH", "/storages/{id}", "storage.update", true, (*API).handlePatchStorage},
	{"DELETE", "/storages/{id}", "storage.delete", true, (*API).handleDeleteStorage},
	{"GET", "/s3-storages", "s3storage.list", true, (*API).handleListS3Storages},
	{"POST", "/s3-storages", "s3storage.create", true, (*API).handleCreateS3Storage},
	{"GET", "/s3-storages/{id}", "s3storage.list", true, (*API).handleGetS3Storage},
	{"PATCH", "/s3-storages/{id}", "s3storage.update", true, (*API).handlePatchS3Storage},
	{"DELETE", "/s3-storages/{id}", "s3storage.delete", true, (*API).handleDeleteS3Storage},
	{"POST", "/s3-storages/{id}/validate", "s3storage.update", true, (*API).handleValidateS3Storage},

	{"GET", "/notifications/email", "notification.list", true, (*API).handleGetNotificationEmail},
	{"PATCH", "/notifications/email", "notification.update", true, (*API).handleUpdateNotificationEmail},
	{"GET", "/notifications/telegram", "notification.list", true, (*API).handleGetNotificationTelegram},
	{"PATCH", "/notifications/telegram", "notification.update", true, (*API).handleUpdateNotificationTelegram},
	{"GET", "/notifications/discord", "notification.list", true, (*API).handleGetNotificationDiscord},
	{"PATCH", "/notifications/discord", "notification.update", true, (*API).handleUpdateNotificationDiscord},
	{"GET", "/notifications/slack", "notification.list", true, (*API).handleGetNotificationSlack},
	{"PATCH", "/notifications/slack", "notification.update", true, (*API).handleUpdateNotificationSlack},
	{"GET", "/notifications/pushover", "notification.list", true, (*API).handleGetNotificationPushover},
	{"PATCH", "/notifications/pushover", "notification.update", true, (*API).handleUpdateNotificationPushover},
	{"GET", "/notifications/webhook", "notification.list", true, (*API).handleGetNotificationWebhook},
	{"PATCH", "/notifications/webhook", "notification.update", true, (*API).handleUpdateNotificationWebhook},
	{"POST", "/notifications/test", "notification.update", true, (*API).handleTestNotification},

	{"GET", "/team/envs", "env.list", true, (*API).handleListTeamEnv},
	{"POST", "/team/envs", "env.set", true, (*API).handleCreateTeamEnv},
	{"PATCH", "/team/envs/{envId}", "env.set", true, (*API).handlePatchTeamEnv},
	{"DELETE", "/team/envs/{envId}", "env.set", true, (*API).handleDeleteTeamEnv},
	{"GET", "/projects/{projectId}/envs", "env.list", true, (*API).handleListProjectSharedEnv},
	{"POST", "/projects/{projectId}/envs", "env.set", true, (*API).handleCreateProjectSharedEnv},
	{"PATCH", "/projects/{projectId}/envs/{envId}", "env.set", true, (*API).handlePatchProjectSharedEnv},
	{"DELETE", "/projects/{projectId}/envs/{envId}", "env.set", true, (*API).handleDeleteProjectSharedEnv},
	{"GET", "/projects/{projectId}/environments/{envId}/envs", "env.list", true, (*API).handleListEnvSharedEnv},
	{"POST", "/projects/{projectId}/environments/{envId}/envs", "env.set", true, (*API).handleCreateEnvSharedEnv},
	{"GET", "/servers/{id}/envs", "env.list", true, (*API).handleListServerEnv},
	{"POST", "/servers/{id}/envs", "env.set", true, (*API).handleCreateServerEnv},
	{"PATCH", "/servers/{id}/envs/{envId}", "env.set", true, (*API).handlePatchServerEnv},
	{"DELETE", "/servers/{id}/envs/{envId}", "env.set", true, (*API).handleDeleteServerEnv},

	{"GET", "/settings", "settings.list", true, (*API).handleGetInstanceSettings},
	{"PATCH", "/settings", "settings.update", true, (*API).handlePatchInstanceSettings},
	{"GET", "/settings/email", "settings.list", true, (*API).handleGetSettingsEmail},
	{"PATCH", "/settings/email", "settings.update", true, (*API).handleUpdateSettingsEmail},
	{"GET", "/settings/oauth", "settings.list", true, (*API).handleGetSettingsOAuth},
	{"PATCH", "/settings/oauth", "settings.update", true, (*API).handleUpdateSettingsOAuth},
	{"GET", "/settings/oauth/{provider}", "settings.list", true, (*API).handleGetSettingsOAuthProvider},
	{"PATCH", "/settings/oauth/{provider}", "settings.update", true, (*API).handleUpdateSettingsOAuthProvider},
	{"GET", "/settings/backup", "settings.list", true, (*API).handleGetSettingsBackup},
	{"PATCH", "/settings/backup", "settings.update", true, (*API).handleUpdateSettingsBackup},
	{"GET", "/settings/advanced", "settings.list", true, (*API).handleGetSettingsAdvanced},
	{"PATCH", "/settings/advanced", "settings.update", true, (*API).handleUpdateSettingsAdvanced},
	{"GET", "/settings/updates", "settings.list", true, (*API).handleGetSettingsUpdates},

	{"GET", "/profile", "", true, (*API).handleGetProfile},
	{"PATCH", "/profile", "", true, (*API).handlePatchProfile},
	{"GET", "/profile/avatar", "", true, (*API).handleGetProfileAvatar},
	{"POST", "/profile/avatar", "", true, (*API).handleUploadProfileAvatar},
	{"DELETE", "/profile/avatar", "", true, (*API).handleDeleteProfileAvatar},
	{"GET", "/profile/appearance", "", true, (*API).handleGetProfileAppearance},
	{"PATCH", "/profile/appearance", "", true, (*API).handlePatchProfileAppearance},

	{"GET", "/team/admin", "org.audit", true, (*API).handleTeamAdmin},
	{"GET", "/team/danger", "org.settings", true, (*API).handleTeamDanger},
	{"GET", "/team/audit-log", "org.audit", true, (*API).handleTeamAuditLog},
	{"GET", "/invitations/{uuid}", "", false, (*API).handleGetInvitation},
	{"POST", "/invitations/{uuid}/accept", "", true, (*API).handleAcceptInvitation},
	{"POST", "/invitations/{uuid}/revoke", "member.remove", true, (*API).handleRevokeInvitation},

	{"GET", "/terminal", "", true, (*API).handleTerminalPage},
	{"POST", "/terminal/auth", "", true, (*API).handleTerminalAuth},
	{"POST", "/terminal/auth/ips", "", true, (*API).handleTerminalAuthIPs},
	{"POST", "/terminal/session", "", true, (*API).handleTerminalSession},

	{"POST", "/upload/backup/{databaseUuid}", "backup.create", true, (*API).handleUploadBackup},
	{"GET", "/download/backup/{executionId}", "backup.read", true, (*API).handleDownloadBackup},
	{"GET", "/download/volume-backup/{executionId}", "backup.read", true, (*API).handleDownloadVolumeBackup},

	{"GET", "/realtime", "event.read", true, (*API).handleRealtime},
	{"GET", "/verify", "", true, (*API).handleVerify},
	{"POST", "/auth/link", "", false, (*API).handleMagicLinkAccept},
	{"GET", "/onboarding", "onboarding.read", true, (*API).handleOnboarding},
	{"GET", "/admin", "admin.read", true, (*API).handleAdminIndex},
	{"GET", "/subscription/new", "billing.read", true, (*API).handleNewSubscription},
	// Go's ServeMux has no optional-wildcard syntax — a "{name?}" segment
	// panics at registration. The optional tag is therefore two literal
	// routes, and the literal "/tags" must stay above its wildcard sibling.
	{"GET", "/tags", "project.read", true, (*API).handleTags},
	{"GET", "/tags/{tagName}", "project.read", true, (*API).handleTags},
	{"GET", "/sources", "source.list", true, (*API).handleListSources},
	{"GET", "/source/github/{uuid}", "source.list", true, (*API).handleGitHubSource},
	{"DELETE", "/source/github/{uuid}", "source.delete", true, (*API).handleDeleteGitHubSource},
	{"GET", "/source/github/{uuid}/permissions", "source.list", true, (*API).handleGitHubSourcePermissions},
	{"GET", "/source/github/{uuid}/resources", "source.list", true, (*API).handleGitHubSourceResources},
	{"GET", "/source/gitlab/{uuid}", "source.list", true, (*API).handleGitLabSource},

	// Databases as first-class (SRS §20 workload kind)
	{"GET", "/databases", "database.list", true, (*API).handleListDatabases},
	{"POST", "/databases", "database.create", true, (*API).handleCreateDatabase},
	{"GET", "/databases/{id}", "database.list", true, (*API).handleGetDatabase},
	{"PATCH", "/databases/{id}", "database.update", true, (*API).handlePatchDatabase},
	{"DELETE", "/databases/{id}", "database.delete", true, (*API).handleDeleteDatabase},
	{"GET", "/databases/{id}/backups", "database.list", true, (*API).handleListDatabaseBackups},
	{"POST", "/databases/{id}/backups", "database.update", true, (*API).handleCreateDatabaseBackup},
	{"GET", "/databases/{id}/backups/{backupUuid}", "database.list", true, (*API).handleGetDatabaseBackup},
	{"GET", "/databases/{id}/backups/{backupUuid}/executions", "database.list", true, (*API).handleListDatabaseBackupExecutions},
	{"GET", "/databases/{id}/backups/{backupUuid}/s3", "database.list", true, (*API).handleDatabaseBackupS3},
	{"GET", "/databases/{id}/backups/{backupUuid}/retention", "database.list", true, (*API).handleDatabaseBackupRetention},
	{"GET", "/databases/{id}/backups/{backupUuid}/danger", "database.list", true, (*API).handleDatabaseBackupDanger},
	{"POST", "/databases/{id}/import-backup", "database.update", true, (*API).handleDatabaseImportBackup},
}

// ============================================================================
// Route registration + permission map
// ============================================================================

func buildRoutePerms() map[string]string {
	out := make(map[string]string, len(apiRoutes))
	for _, rd := range apiRoutes {
		if rd.perm != "" {
			out[rd.method+" "+rd.pattern] = rd.perm
		}
	}
	return out
}

func (a *API) Routes(mux *http.ServeMux) {
	for _, rd := range apiRoutes {
		h, auth := rd.handler, rd.auth
		if auth {
			mux.HandleFunc(rd.method+" "+rd.pattern, a.auditAuth(func(w http.ResponseWriter, r *http.Request) {
				h(a, w, r)
			}))
		} else {
			mux.HandleFunc(rd.method+" "+rd.pattern, func(w http.ResponseWriter, r *http.Request) {
				r = withRequestID(w, r)
				h(a, w, r)
			})
		}
	}
}

var (
	routePermsOnce sync.Once
	routePerms     map[string]string
)

func routePermsMap() map[string]string {
	routePermsOnce.Do(func() {
		routePerms = buildRoutePerms()
	})
	return routePerms
}

func permForRoute(r *http.Request) string {
	return routePermsMap()[r.Pattern]
}

// ============================================================================
// Request-ID middleware
// ============================================================================

type reqIDCtxKey struct{}

func currentRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(reqIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}

func withRequestID(w http.ResponseWriter, r *http.Request) *http.Request {
	id := r.Header.Get("X-Request-ID")
	if id == "" {
		id = store.NewID()
	}
	w.Header().Set("X-Request-ID", id)
	return r.WithContext(context.WithValue(r.Context(), reqIDCtxKey{}, id))
}

// ============================================================================
// Idempotency wrapper
// ============================================================================

type recordingWriter struct {
	http.ResponseWriter
	status int
	body   []byte
	wrote  bool
}

func (w *recordingWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	w.body = append(w.body, b...)
	return w.ResponseWriter.Write(b)
}

func (w *recordingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *API) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete) {
			next(w, r)
			return
		}
		scoped := r.Method + " " + r.Pattern + " " + key
		if rec, ok := a.store.GetIdempotency(scoped); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Idempotent-Replay", "true")
			w.WriteHeader(rec.StatusCode)
			_, _ = w.Write([]byte(rec.Response))
			return
		}
		rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		a.store.PutIdempotency(scoped, rec.status, string(rec.body))
	}
}

// ============================================================================
// Pagination + ETag helpers
// ============================================================================

func paginate[T any](w http.ResponseWriter, r *http.Request, items []T) []T {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("cursor")); err == nil && v > 0 {
		offset = v
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	w.Header().Set("X-Next-Cursor", next)
	return items[offset:end]
}

func etagFresh(w http.ResponseWriter, r *http.Request, body []byte) bool {
	sum := sha256.Sum256(body)
	tag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", tag)
	return r.Header.Get("If-None-Match") == tag
}

// ============================================================================
// Auth middleware
// ============================================================================

type authCtxKey struct{}

func currentAuthContext(r *http.Request) auth.AuthContext {
	if c, ok := r.Context().Value(authCtxKey{}).(auth.AuthContext); ok {
		return c
	}
	return auth.AuthContext{}
}

type rbacCtxKey struct{}

type principal struct {
	username string
	role     string
}

func currentPrincipal(r *http.Request) principal {
	if p, ok := r.Context().Value(rbacCtxKey{}).(principal); ok {
		return p
	}
	return principal{}
}

func currentRole(r *http.Request) string { return currentPrincipal(r).role }
func currentUser(r *http.Request) string { return currentPrincipal(r).username }

// csrfExemptPaths are public auth endpoints (pre-authentication by design).
var csrfExemptPaths = map[string]bool{
	"/csrf":                 true,
	"/login":                true,
	"/auth/login":           true,
	"/auth/signup":          true,
	"/auth/password/forgot": true,
	"/auth/password/reset":  true,
	"/auth/ldap/login":      true,
}

func csrfExempt(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	return csrfExemptPaths[r.URL.Path]
}

// auditableMethod reports whether a method mutates state. Reads are not
// audited: auditing every GET would drown the audit table and destroy its
// value as a record of sensitive operations (SRS §26).
func auditableMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder captures the response status without buffering the body, so
// streaming endpoints (SSE, logs) pass through untouched.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.wrote = true
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps SSE/streaming handlers working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// auditAuth wraps an authenticated route. It delegates the whole auth chain
// (request ID, bearer resolution, rate limit, CSRF, RBAC, idempotency) to
// auth(), then records exactly one audit row for every state-changing request,
// including requests auth() rejected — so denials are attributable too, not
// just successes (ARCH §8: every sensitive op leaves an audit row).
//
// The principal is captured from the inner request because auth() is what
// resolves it and stores it in the request context.
func (a *API) auditAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auditableMethod(r.Method) {
			a.auth(next)(w, r)
			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		var p principal
		var reqID string
		a.auth(func(w http.ResponseWriter, r *http.Request) {
			p = currentPrincipal(r)
			reqID = currentRequestID(r)
			next(w, r)
		})(rec, r)

		action := r.Method + " " + r.Pattern
		if r.Pattern == "" {
			action = r.Method + " " + r.URL.Path
		}
		outcome := "allowed"
		switch {
		case rec.status == http.StatusUnauthorized, rec.status == http.StatusForbidden:
			outcome = "denied"
		case rec.status >= http.StatusInternalServerError:
			outcome = "failed"
		}
		actorType, actorID := "user", p.username
		if actorID == "" {
			actorType, actorID = "anonymous", "anonymous"
		}
		if reqID == "" {
			reqID = rec.Header().Get("X-Request-ID")
		}
		ip := r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
		// AppendAuditRecord logs and swallows its own error by design: an audit
		// write must never fail the request it records. The nil guard keeps
		// that promise even when the API is built without a store (as the
		// route-registration tests do) — recording an audit row is never
		// allowed to panic the data path.
		if a.store != nil {
			_ = a.store.AppendAuditRecord(actorType, actorID, action, r.URL.Path, reqID, ip, outcome)
		}
	}
}

func (a *API) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = withRequestID(w, r)
		tok := bearerToken(r)
		p := principal{}
		var forcedScope *auth.AuthContext
		if tok != "" {
			p, forcedScope = a.resolveBearer(tok)
		}
		if p.username == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if a.rateLimit > 0 && !a.allowRate(r.RemoteAddr) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		if !csrfExempt(r) {
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" || !constantTimeEqual(csrf, a.csrfToken) {
				writeError(w, http.StatusForbidden, "invalid or missing CSRF token")
				return
			}
		}
		if perm := permForRoute(r); perm != "" {
			if !a.granted(r, p, perm) {
				writeError(w, http.StatusForbidden, "missing permission: "+perm)
				return
			}
		}
		ctx := context.WithValue(r.Context(), rbacCtxKey{}, p)
		scopeType, scopeID := auth.ResolveScope("", r.Header.Get(auth.TenantHeader))
		if forcedScope != nil {
			scopeType, scopeID = forcedScope.ScopeType, forcedScope.ScopeID
		}
		actx := auth.AuthContext{
			PrincipalType: "user",
			PrincipalID:   p.username,
			ScopeType:     scopeType,
			ScopeID:       scopeID,
		}
		ctx = context.WithValue(ctx, authCtxKey{}, actx)
		a.idempotent(func(w http.ResponseWriter, r *http.Request) {
			next(w, r)
		})(w, r.WithContext(ctx))
	}
}

func (a *API) resolveBearer(tok string) (principal, *auth.AuthContext) {
	if strings.Count(tok, ".") == 2 && a.jwtKey.KID != "" {
		if claims, err := a.jwtKey.Verify(tok, "porter-api"); err == nil && claims.Subject != "" {
			p := principal{username: claims.Subject}
			if u, ok := a.store.GetUserByUsername(claims.Subject); ok {
				p.role = u.Role
			}
			scopeType, scopeID := auth.ResolveScope("", claims.Tenant)
			return p, &auth.AuthContext{
				PrincipalType: "user", PrincipalID: claims.Subject,
				ScopeType: scopeType, ScopeID: scopeID,
			}
		}
	}
	if sess, ok := a.store.GetSessionByToken(hashToken(tok)); ok {
		p := principal{username: sess.PrincipalID}
		if u, ok := a.store.GetUserByUsername(sess.PrincipalID); ok {
			p.role = u.Role
		}
		return p, &auth.AuthContext{
			PrincipalType: sess.PrincipalType, PrincipalID: sess.PrincipalID,
			ScopeType: sess.ScopeType, ScopeID: sess.ScopeID,
		}
	}
	if u, ok := a.store.GetUserByToken(tok); ok && u.Username != "" {
		return principal{username: u.Username, role: u.Role}, nil
	}
	return principal{}, nil
}

func (a *API) granted(r *http.Request, p principal, perm string) bool {
	scopeType, scopeID := "platform", ""
	if projID := r.PathValue("projectId"); projID != "" {
		scopeType, scopeID = "project", projID
	} else if orgID := r.Header.Get(HeaderOrgID); orgID != "" {
		scopeType, scopeID = "org", orgID
		if a.store.HasCapability("user", p.username, perm, scopeType, scopeID) {
			return true
		}
		if a.store.ScopedDeny("user", p.username, perm, scopeType, scopeID) {
			return false
		}
		if role := a.store.OrgRoleForUser(orgID, p.username); role != "" && a.store.HasOrgPermission(orgID, p.username, perm) {
			return true
		}
	}
	if a.store.HasCapability("user", p.username, perm, scopeType, scopeID) {
		return true
	}
	if a.store.ScopedDeny("user", p.username, perm, scopeType, scopeID) {
		return false
	}
	if scopeType == "project" {
		return a.store.HasProjectPermission(scopeID, p.username, perm)
	}
	return a.store.HasPermissionAnywhere(p.username, perm)
}

// ============================================================================
// Core handlers — orgs / groups / projects / users
// ============================================================================

func (a *API) handleGetCurrentOrg(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	org, ok := a.store.GetOrg(orgID)
	if !ok {
		writeError(w, http.StatusNotFound, "no org set; use X-Porter-Org-Id header")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": org})
}

func (a *API) handlePatchCurrentOrg(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	org, ok := a.store.GetOrg(orgID)
	if !ok {
		writeError(w, http.StatusNotFound, "no org set")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = readJSON(r, &req)
	if req.Name != "" {
		org.Name = req.Name
	}
	_ = a.store.PutOrg(org)
	writeJSON(w, http.StatusOK, map[string]any{"org": org})
}

func (a *API) handleDeleteCurrentOrg(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	org, ok := a.store.GetOrg(orgID)
	if !ok {
		writeError(w, http.StatusNotFound, "no org set")
		return
	}
	if org.IsDefault {
		writeError(w, http.StatusForbidden, "cannot delete your default org")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	deleted, err := a.store.DeleteOrgCascade(orgID, force)
	if errors.Is(err, store.ErrOrgHasRunningVMs) {
		writeError(w, http.StatusConflict, "organization still has running VMs; pass ?force=true to delete anyway")
		return
	}
	if errors.Is(err, store.ErrOrgIsDefault) {
		writeError(w, http.StatusForbidden, "cannot delete your default org")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete organization: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "org": org.Name, "deleted": deleted})
}

func (a *API) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	writeJSON(w, http.StatusOK, map[string]any{"org_id": orgID, "members": a.store.ListOrgMembers(orgID)})
}

// handleAddOrgMember adds a member to the current org, creating the user first
// when the username is new. Recovered from the baseline commit (56bf8a9) and
// re-enabled once DefaultGlobalRoleID existed.
func (a *API) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	_ = readJSON(r, &req)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role == "" {
		req.Role = a.store.DefaultRoleID()
	}
	if req.Role == "" {
		writeError(w, http.StatusServiceUnavailable, "no default role is seeded; create one via POST /roles")
		return
	}
	if _, ok := a.store.GetRole(req.Role); !ok {
		writeError(w, http.StatusBadRequest, "unknown role: "+req.Role)
		return
	}
	u, exists := a.store.GetUserByUsername(req.Username)
	if !exists {
		if req.Password == "" {
			writeError(w, http.StatusBadRequest, "password is required when creating a new user")
			return
		}
		salt := store.NewID()
		globalRole := a.store.DefaultGlobalRoleID()
		if globalRole == "" {
			globalRole = "member"
		}
		u = &types.User{
			ID: store.NewID(), Username: req.Username, Role: globalRole,
			Salt: salt, PasswordHash: passwordHash(req.Password, salt), CreatedAt: time.Now(),
		}
		a.store.PutUser(u)
	}
	orgID := a.orgIDFromHeader(r)
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "organization context is required")
		return
	}
	if err := a.store.AddOrgMember(orgID, u.ID, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "add organization member: "+err.Error())
		return
	}
	a.store.AppendDaemonLog(fmt.Sprintf("org member %s added (role %s)", req.Username, req.Role))
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "added",
		"member": map[string]any{
			"org_id": orgID, "user_id": u.ID, "username": u.Username, "role": req.Role,
		},
	})
}

func (a *API) handlePatchOrgMember(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	var req struct {
		Role string `json:"role"`
	}
	_ = readJSON(r, &req)
	u, ok := a.store.GetUserByUsername(username)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if req.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}
	if _, exists := a.store.GetRole(req.Role); !exists {
		writeError(w, http.StatusBadRequest, "unknown role: "+req.Role)
		return
	}
	if !a.store.SetOrgMemberRole(a.orgIDFromHeader(r), u.ID, req.Role) {
		writeError(w, http.StatusNotFound, "organization membership not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "username": username, "role": req.Role})
}

func (a *API) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	u, ok := a.store.GetUserByUsername(username)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	org, orgOK := a.store.GetOrg(a.orgIDFromHeader(r))
	if orgOK && org.OwnerID == u.ID {
		writeError(w, http.StatusForbidden, "organization owner cannot be removed")
		return
	}
	if !a.store.DeleteOrgMember(a.orgIDFromHeader(r), u.ID) {
		writeError(w, http.StatusNotFound, "organization membership not found")
		return
	}
	a.store.AppendDaemonLog("org member " + username + " removed")
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed", "username": username})
}

func (a *API) handleOrgAudit(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	events, total, err := a.store.ListAuditEvents(store.AuditFilter{
		Actor:    q.Get("actor"),
		Action:   q.Get("action"),
		Resource: q.Get("resource"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list audit events: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org_id": orgID, "events": events, "total": total})
}

// handleOrgTransfer — resolves email → user ID before storing (the earlier
// version stored the raw email string in Org.OwnerID, breaking every
// downstream `org.OwnerID == user.ID` check).
func (a *API) handleOrgTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewOwnerEmail string `json:"new_owner_email"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.NewOwnerEmail == "" {
		writeError(w, http.StatusBadRequest, "new_owner_email is required")
		return
	}
	u, ok := a.store.GetUserByEmail(req.NewOwnerEmail)
	if !ok {
		writeError(w, http.StatusNotFound, "no user with that email")
		return
	}
	orgID := a.orgIDFromHeader(r)
	org, ok := a.store.GetOrg(orgID)
	if !ok {
		writeError(w, http.StatusNotFound, "no org set")
		return
	}
	org.OwnerID = u.ID
	if err := a.store.PutOrg(org); err != nil {
		writeError(w, http.StatusInternalServerError, "transfer org: "+err.Error())
		return
	}
	a.store.AppendDaemonLog(fmt.Sprintf("org %s transferred to %s", org.Name, req.NewOwnerEmail))
	writeJSON(w, http.StatusOK, map[string]any{"status": "transferred", "to": req.NewOwnerEmail, "org": org})
}

// handleOrgEvents — merges events across ALL projects in the org (the earlier
// version bailed after the first project with a single event).
func (a *API) handleOrgEvents(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	perProject := 10
	total := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		total = v
	}
	if total > 500 {
		total = 500
	}
	var merged []*types.HealthEvent
	for _, p := range a.store.ListProjectsByOrg(orgID) {
		events := a.store.ListHealthEvents(p.ID, perProject)
		merged = append(merged, events...)
		if len(merged) >= total {
			break
		}
	}
	if len(merged) > total {
		merged = merged[:total]
	}
	if merged == nil {
		merged = []*types.HealthEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"org_id": orgID, "events": merged})
}

func (a *API) handleListGroups(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	writeJSON(w, http.StatusOK, map[string]any{"groups": a.store.ListGroups(orgID)})
}

func (a *API) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	g := &types.Group{ID: store.NewID(), OrgID: orgID, Name: req.Name, CreatedAt: time.Now()}
	if err := a.store.PutGroup(g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"group": g})
}

func (a *API) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("groupId")
	if g, ok := a.store.GetGroup(groupID); ok {
		writeJSON(w, http.StatusOK, map[string]any{"group": g})
		return
	}
	writeError(w, http.StatusNotFound, "group not found")
}

func (a *API) handlePatchGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("groupId")
	g, ok := a.store.GetGroup(groupID)
	if !ok {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name != "" {
		g.Name = req.Name
	}
	if err := a.store.PutGroup(g); err != nil {
		writeError(w, http.StatusInternalServerError, "update group: "+err.Error())
		return
	}
	// Envelope matches handleCreateGroup/handleGetGroup so clients can rely on
	// resp["group"] across all group endpoints.
	writeJSON(w, http.StatusOK, map[string]any{"group": g})
}

func (a *API) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("groupId")
	deleted := a.store.DeleteGroup(id)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id, "deleted": deleted})
}

func (a *API) handleGroupProjects(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("groupId")
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id": groupID,
		"projects": a.store.ListProjectsInGroup(groupID),
	})
}

func (a *API) handleAddGroupProject(w http.ResponseWriter, r *http.Request) {
	if err := a.store.AddProjectToGroup(r.PathValue("groupId"), r.PathValue("projectId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "added"})
}

func (a *API) handleRemoveGroupProject(w http.ResponseWriter, r *http.Request) {
	if err := a.store.RemoveProjectFromGroup(r.PathValue("groupId"), r.PathValue("projectId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

// ============================================================================
// Project core (createProjectFrom + bootReplica + secrets + manifest)
// ============================================================================

type createProjectReq struct {
	Name string `json:"name"`

	Image         string             `json:"image"`
	GitURL        string             `json:"git_url"`
	Branch        string             `json:"branch"`
	ComposeYAML   string             `json:"compose_yaml"`
	OrgID         string             `json:"org_id"`
	GroupID       string             `json:"group_id"`
	VCPUs         int                `json:"vcpus"`
	MemMiB        int                `json:"mem_mib"`
	Env           map[string]string  `json:"env"`
	Ports         []types.Port       `json:"ports"`
	Replicas      int                `json:"replicas"`
	HostMountPath string             `json:"host_mount_path"`
	VolumeID      string             `json:"volume_id"`
	Healthcheck   *types.Healthcheck `json:"healthcheck"`
	RestartPolicy string             `json:"restart_policy"`
	SSHEnabled    bool               `json:"ssh_enabled"`
	deployment    *types.Deployment
}

func (a *API) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.OrgID == "" {
		req.OrgID = a.orgIDFromHeader(r)
	}
	a.createProjectFrom(w, req, currentUser(r))
}

func (a *API) createProjectFrom(w http.ResponseWriter, req createProjectReq, createProjectCreator string) {
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if createProjectCreator == "" {
		writeError(w, http.StatusUnauthorized, "authenticated identity required")
		return
	}
	quotaOrg := req.OrgID
	if quotaOrg == "" {
		if orgs := a.store.ListOrgs(); len(orgs) > 0 {
			quotaOrg = orgs[0].ID
		}
	}
	_, limits := a.store.ResolvePlanForProject("", quotaOrg)
	owned := int64(0)
	for _, p := range a.store.ListProjects() {
		if p != nil && p.CreatedBy == createProjectCreator {
			owned++
		}
	}
	if lim, over := store.OverQuota(limits, "max_projects", owned+1); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_projects=%d (you own %d projects)", lim, owned))
		return
	}
	if req.Image == "" && req.GitURL == "" && a.hostConfig != nil {
		req.Image = a.hostConfig.BaseImageRef
	}
	if req.GitURL == "" {
		if req.Image == "" {
			writeError(w, http.StatusBadRequest, "image is required; choose a registered base:// or custom:// microVM image")
			return
		}
		if !a.knownDirectImage(req.Image) {
			writeError(w, http.StatusUnprocessableEntity, "image is not a registered direct Firecracker manifest; Docker/OCI references are not bootable")
			return
		}
		if err := a.ensureImagePulled(r.Context(), req.Image); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if req.Replicas < 1 {
		req.Replicas = 1
	}
	if lim, over := store.OverQuota(limits, "max_vms", int64(req.Replicas)); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_vms=%d (requested %d replicas)", lim, req.Replicas))
		return
	}
	if over, lim, _ := billing.CheckEntitlement(limits, billing.EntMaxVCPU, 0, int64(req.VCPUs)); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_vcpu=%d (requested %d vCPUs)", lim, req.VCPUs))
		return
	}
	if over, lim, _ := billing.CheckEntitlement(limits, billing.EntMaxMemoryMiB, 0, int64(req.MemMiB)); over {
		writeError(w, http.StatusForbidden, fmt.Sprintf("quota exceeded: max_memory_mib=%d (requested %d MiB)", lim, req.MemMiB))
		return
	}
	if req.RestartPolicy == "" {
		req.RestartPolicy = "on-failure"
	}
	orgID := req.OrgID
	if orgID == "" {
		if orgs := a.store.ListOrgs(); len(orgs) > 0 {
			orgID = orgs[0].ID
		}
	}
	projID := store.NewID()
	source := "image"
	if req.GitURL != "" {
		source = "git"
	}
	proj := &types.Project{
		ID:              projID,
		OrgID:           orgID,
		Name:            req.Name,
		Source:          source,
		Image:           req.Image,
		Network:         "10.42.0.0/16",
		HostMountPath:   req.HostMountPath,
		ReplicasDesired: req.Replicas,
		Replicas:        req.Replicas,
		RestartPolicy:   req.RestartPolicy,
		Healthcheck:     req.Healthcheck,
		Env:             req.Env,
		SSHEnabled:      req.SSHEnabled,
		ServicePools:    map[string]*types.ServicePool{},
		CreatedAt:       time.Now(),
	}
	if a.net != nil {
		if subnet, err := a.net.AllocateSubnet(); err == nil {
			proj.Network = subnet.String()
		}
	}
	proj.CreatedBy = createProjectCreator
	if err := a.store.PutProject(proj); err != nil {
		writeError(w, http.StatusInternalServerError, "project could not be persisted: "+err.Error())
		return
	}
	if u, ok := a.store.GetUserByUsername(createProjectCreator); ok {
		if role := a.store.ProjectMemberRoleID(); role != "" {
			a.store.PutProjectMember(&types.ProjectMember{
				ProjectID: projID, UserID: u.ID, Role: role, CreatedAt: time.Now(),
			})
		}
	}
	if req.GroupID != "" {
		_ = a.store.AddProjectToGroup(req.GroupID, projID)
	}

	if req.GitURL != "" {
		b := &types.Build{
			ID: store.NewID(), ProjectID: projID, GitURL: req.GitURL,
			Branch: orDefault(req.Branch, "main"), BuildStatus: "building", CreatedAt: time.Now(),
		}
		a.store.PutBuild(b)
		a.store.AppendBuildLog(projID, "git project queued: "+req.GitURL)
		a.runGitBuildCtx(b, nil, nil)
		for i := 0; i < req.Replicas; i++ {
			rr := req
			rr.Image = b.Image
			if rr.Image != "" {
				rr.Name = req.Name
				a.bootReplica(proj, rr, i)
			}
		}
		_ = a.store.CreateDeployment(&types.Deployment{
			ID: store.NewID(), ProjectID: projID,
			BuildStatus: b.BuildStatus, ImageDigest: b.Image,
			GitURL: req.GitURL, CreatedAt: time.Now(),
		})
		a.store.AppendDaemonLog(fmt.Sprintf("project %s created via git (%s)", req.Name, req.GitURL))
		writeJSON(w, http.StatusAccepted, map[string]any{"project": proj, "status": "building"})
		return
	}

	for i := 0; i < req.Replicas; i++ {
		a.bootReplica(proj, req, i)
	}
	_ = a.store.CreateDeployment(&types.Deployment{
		ID: store.NewID(), ProjectID: projID,
		BuildStatus: "ready", ImageDigest: req.Image, CreatedAt: time.Now(),
	})
	a.store.AppendBuildLog(projID, fmt.Sprintf("deployed %s (%s) with %d replica(s)", req.Name, req.Image, req.Replicas))
	a.store.AppendDaemonLog(fmt.Sprintf("project %s created (%s)", req.Name, req.Image))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": proj, "status": "deploying"})
}

func (a *API) knownDirectImage(ref string) bool {
	if strings.HasPrefix(ref, "docker://") || strings.HasPrefix(ref, "oci://") || strings.Contains(ref, "@sha256:") {
		return false
	}
	for _, gi := range a.store.ListGoldenImages() {
		if gi.Image == ref || gi.Name == ref {
			return true
		}
	}
	if a.catalog != nil {
		for _, manifest := range a.catalog.All() {
			if manifest.Image == ref || manifest.ID == ref || manifest.Name == ref {
				return true
			}
		}
	}
	// A catalog entry counts as known even before it is downloaded: it is a
	// real, digest-pinned image, and ensureImagePulled decides whether it can
	// be fetched now or must be pulled explicitly first.
	if a.remoteCat.Enabled() {
		if _, ok := a.remoteCat.Lookup(context.Background(), ref); ok {
			return true
		}
	}
	return false
}

// ensureImagePulled guarantees that an image chosen from the upstream catalog
// has local artifacts before a boot is attempted. Without this the boot would
// fail later with a confusing "requires a readable rootfs.ext4"; here the
// caller gets the exact reason and the endpoint that fixes it.
//
// Porter keeps no guest images in the repository, so a catalog entry is
// downloaded either on demand (catalog_auto_pull) or when the operator pulls it.
func (a *API) ensureImagePulled(ctx context.Context, ref string) error {
	// Already bootable locally?
	for _, im := range a.imageManifests(ctx) {
		if im.Image != ref && im.ID != ref && im.Name != ref {
			continue
		}
		if im.Rootfs == "" {
			continue
		}
		if _, err := os.Stat(im.Rootfs); err == nil {
			return nil
		}
	}

	entry, ok := a.remoteCat.Lookup(ctx, ref)
	if !ok {
		return nil // not a catalog entry; the other validators own this case
	}
	if a.hostConfig == nil || !a.hostConfig.CatalogAutoPull {
		return fmt.Errorf("image %q is in the image catalog but has not been downloaded on this host; POST /images/%s/pull to fetch it, or set catalog_auto_pull = true under [images] in porter.toml", ref, ref)
	}

	dir := a.imagesDir()
	if dir == "" {
		return fmt.Errorf("image %q must be downloaded but no image directory is configured (firecracker.images_dir)", ref)
	}
	if _, err := imagecatalog.Pull(ctx, nil, entry, dir); err != nil {
		return fmt.Errorf("could not download image %q: %w", ref, err)
	}
	if reloadable, ok := a.catalog.(interface{ Reload() }); ok {
		reloadable.Reload()
	}
	return nil
}

func (a *API) bootReplica(proj *types.Project, req createProjectReq, idx int) {
	vmID := store.NewID()
	memMiB := req.MemMiB
	if proj.Autoscale != nil && proj.Autoscale.Enabled {
		if proj.Autoscale.MaxMemMiB > 0 && (memMiB <= 0 || memMiB > proj.Autoscale.MaxMemMiB) {
			memMiB = proj.Autoscale.MaxMemMiB
		}
		if proj.Autoscale.MinMemMiB > 0 && memMiB < proj.Autoscale.MinMemMiB {
			memMiB = proj.Autoscale.MinMemMiB
		}
	}
	if lims := a.projectQuotaLimits(proj.ID); len(lims) > 0 {
		if maxMem, ok := lims["max_mem_mib"]; ok && maxMem > 0 && memMiB > int(maxMem) {
			memMiB = int(maxMem)
		}
	}
	env := req.Env
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range a.secretsEnv(proj) {
		if _, exists := env[k]; !exists {
			env[k] = v
		}
	}
	vm := &types.VM{
		ID: vmID, Name: fmt.Sprintf("%s-%d", proj.Name, idx),
		ProjectID: proj.ID, ServiceName: "web",
		State: types.StatePending, HealthStatus: types.HealthChecking,
		Image: req.Image, ReplicaIndex: idx, VCPUs: req.VCPUs, MemMiB: memMiB,
		Ports: req.Ports, Env: env, VolumeID: req.VolumeID,
		Healthcheck: req.Healthcheck, CreatedAt: time.Now(),
	}
	if req.deployment != nil {
		vm.DeploymentID = req.deployment.ID
		vm.DeploymentVersion = req.deployment.VersionLabel
		vm.DeploymentEnv = req.deployment.Environment
		vm.GuestBase = req.deployment.GuestBase
	}
	a.applyImageManifest(vm)
	if proj.Autoscale != nil && proj.Autoscale.Enabled {
		if proj.Autoscale.MaxMemMiB > 0 && vm.MemMiB > proj.Autoscale.MaxMemMiB {
			vm.MemMiB = proj.Autoscale.MaxMemMiB
		}
		if proj.Autoscale.MinMemMiB > 0 && vm.MemMiB < proj.Autoscale.MinMemMiB {
			vm.MemMiB = proj.Autoscale.MinMemMiB
		}
	}
	if lim, over := a.quotaCheck(proj.ID, "max_mem_mib", int64(vm.MemMiB)); over {
		vm.MemMiB = int(lim)
	}
	if vm.Kernel == "" && a.hostConfig != nil {
		vm.Kernel = a.hostConfig.KernelImage
	}
	if vm.RootfsPath == "" || vm.Kernel == "" {
		vm.State = types.StateFailed
		vm.Error = "direct Firecracker deploy requires a registered image with readable rootfs.ext4 and vmlinux artifacts"
		a.store.PutVM(vm)
		return
	}
	if report, err := imagecatalog.ValidateArtifacts(vm.RootfsPath, vm.Kernel); err != nil {
		vm.State = types.StateFailed
		vm.Error = "image artifact validation failed: " + report.Error
		a.store.PutVM(vm)
		return
	}
	a.store.PutVM(vm)
	if req.deployment != nil {
		req.deployment.VMIDs = append(req.deployment.VMIDs, vmID)
		_ = a.store.CreateDeployment(req.deployment)
	} else {
		if proj.VMIDs == nil {
			proj.VMIDs = []string{}
		}
		proj.VMIDs = append(proj.VMIDs, vmID)
	}
	if a.vmm != nil {
		vm.State = types.StateBooting
		a.store.PutVM(vm)
		go func(c types.VM) {
			err := a.vmm.Boot(context.Background(), &c)
			cur, ok := a.store.GetVM(c.ID)
			if !ok || cur == nil {
				return
			}
			switch {
			case err != nil && cur.State != types.StateRunning:
				cur.State = types.StateFailed
				cur.Error = "boot failed: " + err.Error()
			case err == nil && cur.State != types.StateRunning:
				cur.State = types.StateRunning
				if c.IPAddress != "" {
					cur.IPAddress = c.IPAddress
				}
			default:
				return
			}
			a.store.PutVM(cur)
		}(*vm)
	}
	a.store.PutProject(proj)
}

func (a *API) secretsEnv(proj *types.Project) map[string]string {
	merged := map[string]string{}
	if proj.Env != nil {
		for k, v := range proj.Env {
			merged[k] = v
		}
	}
	for _, sec := range a.store.ListSecrets(proj.ID) {
		val, err := a.decryptSecret(sec.ValueEncrypted)
		if err != nil {
			a.store.AppendDaemonLog(fmt.Sprintf("secret %q for project %s could not be decrypted (%v)", sec.Name, proj.ID, err))
			continue
		}
		merged[sec.Name] = val
	}
	return merged
}

func (a *API) applyImageManifest(vm *types.VM) {
	if vm == nil {
		return
	}
	for _, gi := range a.store.ListGoldenImages() {
		if gi.Image == vm.Image || gi.Name == vm.Image || gi.Image == "custom://"+strings.TrimPrefix(vm.Image, "custom://") {
			vm.RootfsPath = gi.Rootfs
			vm.Kernel = gi.Kernel
			if vm.VCPUs == 0 {
				vm.VCPUs = gi.VCPUs
			}
			if vm.MemMiB == 0 {
				vm.MemMiB = gi.MemMiB
			}
			return
		}
	}
	if a.catalog != nil {
		for _, manifest := range a.catalog.All() {
			if manifest.Image == vm.Image || manifest.ID == vm.Image || manifest.Name == vm.Image {
				vm.RootfsPath = manifest.Rootfs
				vm.Kernel = manifest.Kernel
				if vm.VCPUs == 0 {
					vm.VCPUs = manifest.VCPUs
				}
				if vm.MemMiB == 0 {
					vm.MemMiB = manifest.MemMiB
				}
				return
			}
		}
	}
}

func (a *API) handleCreateComposeProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		ComposeYAML string `json:"compose_yaml"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	svcs, perr := compose.ParseCompose(req.ComposeYAML)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}
	if req.Name == "" {
		req.Name = "compose-" + time.Now().Format("20060102-150405")
	}
	orgID := a.orgIDFromHeader(r)
	if orgID == "" {
		if orgs := a.store.ListOrgs(); len(orgs) > 0 {
			orgID = orgs[0].ID
		}
	}
	stack := &types.Stack{
		ID: store.NewID(), Name: req.Name, OrgID: orgID,
		Source: "compose", ComposeYAML: req.ComposeYAML, CreatedAt: time.Now(),
	}
	a.store.PutStack(stack)
	created := make([]*types.Project, 0, len(svcs))
	for _, svc := range svcs {
		proj := &types.Project{
			ID: store.NewID(), OrgID: orgID,
			Name: req.Name + "/" + svc.Name, Source: "compose",
			Image: svc.Image, Network: "10.42.0.0/16",
			ReplicasDesired: svc.Replicas, Replicas: svc.Replicas,
			RestartPolicy: "on-failure", Healthcheck: svc.Healthcheck,
			Env: svc.Env, ComposeYAML: req.ComposeYAML,
			StackID: stack.ID, ComposeService: svc.Name,
			ServicePools: map[string]*types.ServicePool{},
			VMIDs:        []string{}, CreatedAt: time.Now(),
		}
		if a.net != nil {
			if sub, serr := a.net.AllocateSubnet(); serr == nil {
				proj.Network = sub.String()
			}
		}
		spec := createProjectReq{
			Name: proj.Name, Image: svc.Image, Env: svc.Env,
			Ports: svc.Ports, Healthcheck: svc.Healthcheck, Replicas: svc.Replicas,
		}
		if svc.Networks != nil {
			proj.Networks = svc.Networks
		} else if topNetworks := compose.ParseTopLevelNetworks(req.ComposeYAML); len(topNetworks) > 0 {
			proj.Networks = topNetworks
		}
		spec.OrgID = orgID
		a.store.PutProject(proj)
		for i := 0; i < svc.Replicas; i++ {
			a.bootReplica(proj, spec, i)
		}
		a.store.AppendDaemonLog(fmt.Sprintf("compose stack %s: service %s (%s) with %d replica(s)", req.Name, svc.Name, svc.Image, svc.Replicas))
		created = append(created, proj)
	}
	if a.hub != nil {
		a.hub.Broadcast("compose.created", map[string]any{"stack": req.Name, "projects": len(created)})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"stack": stack, "projects": created})
}

func (a *API) handleImportProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Manifest map[string]any `json:"manifest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Manifest == nil {
		writeError(w, http.StatusBadRequest, "manifest is required")
		return
	}
	name, _ := req.Manifest["project"].(string)
	image, _ := req.Manifest["image"].(string)
	if name == "" || image == "" {
		writeError(w, http.StatusBadRequest, "manifest needs project + image")
		return
	}
	cr := createProjectReq{Name: name, Image: image}
	cr.OrgID = a.orgIDFromHeader(r)
	if v, ok := req.Manifest["replicas"].(float64); ok {
		cr.Replicas = int(v)
	}
	if v, ok := req.Manifest["ssh_enabled"].(bool); ok {
		cr.SSHEnabled = v
	}
	a.createProjectFrom(w, cr, currentUser(r))
}

// ============================================================================
// Project members
// ============================================================================

func (a *API) memberUserID(r *http.Request) string {
	return currentUser(r)
}

func (a *API) handleGetProjectMember(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	u, found := a.store.GetUserByUsername(username)
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	pid := a.projectID(r)
	for _, m := range a.store.ListProjectMembers(pid) {
		if m.UserID == u.ID {
			writeJSON(w, http.StatusOK, map[string]any{"member": m, "username": u.Username})
			return
		}
	}
	writeError(w, http.StatusNotFound, "member not found")
}

func (a *API) handlePatchProjectMember(w http.ResponseWriter, r *http.Request) {
	pid := a.projectID(r)
	var req struct {
		Role string `json:"role"`
	}
	_ = readJSON(r, &req)
	u, found := a.store.GetUserByUsername(r.PathValue("username"))
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	updated := false
	for _, m := range a.store.ListProjectMembers(pid) {
		if m.UserID == u.ID {
			m.Role = req.Role
			a.store.PutProjectMember(m)
			updated = true
			break
		}
	}
	if !updated {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": req.Role, "username": r.PathValue("username")})
}

func (a *API) handleRemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	pid := a.projectID(r)
	u, found := a.store.GetUserByUsername(r.PathValue("username"))
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	a.store.DeleteProjectMember(pid, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

func (a *API) handleAddProjectMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Role == "" {
		req.Role = a.store.DefaultRoleID()
	}
	if req.Role == "" {
		writeError(w, http.StatusServiceUnavailable, "no default role is seeded; create one via POST /roles")
		return
	}
	u, found := a.store.GetUserByUsername(req.Username)
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	m := &types.ProjectMember{ProjectID: a.projectID(r), UserID: u.ID, Role: req.Role, CreatedAt: time.Now()}
	a.store.PutProjectMember(m)
	writeJSON(w, http.StatusCreated, map[string]any{"member": m, "username": u.Username})
}

// handleInviteMember persists a pending invite with an EMPTY UserID and the
// target email; a random UUID would orphan the row on accept.
func (a *API) handleInviteMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Username string `json:"username"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Email == "" && req.Username == "" {
		writeError(w, http.StatusBadRequest, "email or username is required")
		return
	}
	defRole := a.store.DefaultRoleID()
	if defRole == "" {
		writeError(w, http.StatusServiceUnavailable, "no default role is seeded; create one via POST /roles")
		return
	}
	if req.Username != "" {
		if u, found := a.store.GetUserByUsername(req.Username); found {
			m := &types.ProjectMember{
				ProjectID: a.projectID(r), UserID: u.ID, Role: defRole,
				Invited: true, CreatedAt: time.Now(),
			}
			a.store.PutProjectMember(m)
			writeJSON(w, http.StatusCreated, map[string]any{
				"member": m, "email": req.Email, "username": u.Username, "invited": true,
			})
			return
		}
	}
	m := &types.ProjectMember{
		ProjectID: a.projectID(r),
		UserID:    "",
		Email:     req.Email,
		Role:      defRole,
		Invited:   true,
		CreatedAt: time.Now(),
	}
	a.store.PutProjectMember(m)
	writeJSON(w, http.StatusCreated, map[string]any{"member": m, "email": req.Email, "invited": true})
}

// ============================================================================
// Users (delete)
// ============================================================================

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	user, found := a.store.GetUserByUsername(username)
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	a.store.DeleteUser(user.ID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}
