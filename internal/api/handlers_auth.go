// Auth, sessions, tokens, API keys, service accounts, profile.
package api

import (
	"net/http"
	"os"
	"time"

	"porter/internal/auth"
	"porter/internal/email"
	"porter/internal/ldap"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Auth / Account
// ============================================================================

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if user, ok := a.store.GetUserByUsername(req.Username); ok {
		if auth.VerifyPassword(req.Password, user.Salt, user.PasswordHash) {
			token, err := a.issueUserToken(user)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to issue token")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "invalid username or password")
}

func (a *API) issueUserToken(user *types.User) (string, error) {
	raw := store.NewID() + store.NewID()
	k := &types.APIKey{
		ID:        store.NewID(),
		UserID:    user.ID,
		Name:      "session",
		TokenHash: hashToken(raw),
		CreatedAt: time.Now(),
	}
	a.store.PutAPIKey(k)
	return raw, nil
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearerToken(r); tok != "" {
		hash := hashToken(tok)
		_ = a.store.RevokeSession(hash)
		_ = a.store.DeleteAPIKeyByHash(hash)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged out"})
}

func (a *API) handleSignup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "note": "single-tenant admin only; add additional users via POST /users"})
}

func (a *API) handlePasswordReset(w http.ResponseWriter, r *http.Request) {
	a.store.AppendDaemonLog("password reset token attempt rejected; no token store configured")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "unsupported",
		"reason": "password reset tokens are not configured",
	})
}

func (a *API) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": currentUser(r)})
}

func (a *API) handleJWKS(w http.ResponseWriter, r *http.Request) {
	if a.jwtKey.KID == "" {
		writeError(w, http.StatusServiceUnavailable, "JWT authentication is not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{a.jwtKey.PublicJWK()}})
}

func (a *API) handleMintToken(w http.ResponseWriter, r *http.Request) {
	if a.jwtKey.KID == "" {
		writeError(w, http.StatusServiceUnavailable, "JWT authentication is not configured")
		return
	}
	me := currentUser(r)
	actx := currentAuthContext(r)
	tok, err := a.jwtKey.Mint(me, "porter-api", "session", actx.ScopeID, 12*time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mint token: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "scope": actx.ScopeType, "tenant": actx.ScopeID})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	if u, ok := a.store.GetUserByUsername(currentUser(r)); ok {
		writeJSON(w, http.StatusOK, u)
		return
	}
	writeError(w, http.StatusUnauthorized, "authenticated user not found")
}

func (a *API) handlePatchMe(w http.ResponseWriter, r *http.Request) {
	u, ok := a.store.GetUserByUsername(currentUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "authenticated user not found")
		return
	}
	var req struct {
		Email       string `json:"email"`
		NotifyOptIn *bool  `json:"notify_opt_in"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Email != "" {
		u.Email = req.Email
	}
	if req.NotifyOptIn != nil {
		u.NotifyOptIn = *req.NotifyOptIn
	}
	a.store.PutUser(u)
	writeJSON(w, http.StatusOK, u)
}

func (a *API) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusForbidden, "self-delete is disabled; an organization owner must remove the account")
}

func (a *API) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := a.store.GetUserByUsername(currentUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "authenticated user not found")
		return
	}
	writeJSON(w, http.StatusOK, a.store.ListAPIKeys(user.ID))
}

func (a *API) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Name == "" {
		req.Name = "default"
	}
	user, ok := a.store.GetUserByUsername(currentUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "authenticated user not found")
		return
	}
	raw := store.NewID()
	k := &types.APIKey{ID: store.NewID(), UserID: user.ID, Name: req.Name, TokenHash: hashToken(raw), CreatedAt: time.Now()}
	a.store.PutAPIKey(k)
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": k, "token": raw})
}

func (a *API) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if a.store.DeleteAPIKey(r.PathValue("keyId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "api key not found")
}

func passwordHash(password, salt string) string { return auth.HashPassword(password, salt) }

// Service accounts (SRS §9): the store layer for service_accounts is not
// wired in this build; routes stay registered for forward compatibility.

func (a *API) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleCreateServiceAccount(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleGetServiceAccount(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handlePatchServiceAccount(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleDeleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleListServiceAccountKeys(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleCreateServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleDeleteServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "service accounts")
}

func (a *API) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	a.store.PutProjectSettings(a.projectID(r), "general", map[string]any{"avatar_url": req.AvatarURL})
	writeJSON(w, http.StatusOK, map[string]any{"status": "avatar updated"})
}

// Profile

func (a *API) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	u, ok := a.store.GetUserByUsername(currentUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "authenticated user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": u.Username, "email": u.Email, "role": u.Role,
		"notify_opt_in": u.NotifyOptIn, "created_at": u.CreatedAt,
	})
}

func (a *API) handlePatchProfile(w http.ResponseWriter, r *http.Request) {
	a.handlePatchMe(w, r)
}

func (a *API) handleGetProfileAvatar(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /profile/avatar")
}

func (a *API) handleUploadProfileAvatar(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /profile/avatar")
}

func (a *API) handleDeleteProfileAvatar(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /profile/avatar")
}

func (a *API) handleGetProfileAppearance(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /profile/appearance")
}

func (a *API) handlePatchProfileAppearance(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /profile/appearance")
}

func (a *API) handleMagicLinkAccept(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /auth/link")
}

// recovered from internal/api/feature_opsx.go
func (a *API) handleLDAPLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || req.Login == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "login and password are required")
		return
	}
	cfg := ldap.Config{
		URL:      os.Getenv("PORTER_LDAP_URL"),
		BaseDN:   os.Getenv("PORTER_LDAP_BASE_DN"),
		Schema:   ldap.Schema(os.Getenv("PORTER_LDAP_SCHEMA")),
		StartTLS: os.Getenv("PORTER_LDAP_STARTTLS") == "1",
	}
	if cfg.Schema == "" {
		cfg.Schema = ldap.SchemaAD
	}
	domain := os.Getenv("PORTER_LDAP_DOMAIN")
	if cfg.URL == "" || cfg.BaseDN == "" {
		writeError(w, http.StatusServiceUnavailable, "LDAP is not configured (PORTER_LDAP_URL/BASE_DN)")
		return
	}
	userDN, groups, err := ldap.Authenticate(ldap.SystemDialer, cfg, req.Login, domain, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid directory credentials")
		return
	}
	// JIT provisioning: known users log in, new ones arrive as members with
	// a personal team. Directory groups map via ldap_group_mappings shape
	// (role assignment beyond member is an operator action).
	username := req.Login
	if user, ok := a.store.GetUserByUsername(username); ok {
		_ = groups // directory group → role mapping (ldap.GroupMapping) is not wired in this build
		token, err := a.issueUserToken(user)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to issue token")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user, "via": "ldap", "dn": userDN})
		return
	}
	user := &types.User{ID: store.NewID(), Username: username, Role: "member", CreatedAt: time.Now()}
	a.store.PutUser(user)
	a.ensurePersonalTeam(r, user)
	token, err := a.issueUserToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": user, "via": "ldap", "dn": userDN})
}

// recovered from internal/api/feature_audit.go
func (a *API) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	u, ok := a.store.GetUserByUsername(currentUser(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unknown user")
		return
	}
	if u.Email == "" {
		writeError(w, http.StatusBadRequest, "user has no email address on file; verification mail cannot be addressed")
		return
	}
	// notify.New always returns a non-nil Mailer and Send() no-ops when
	// disabled — Enable() is the real "transport configured" check. Without
	// it we would answer 200 while nothing left the building.
	if a.mailer == nil || !a.mailer.Enable() {
		writeError(w, http.StatusServiceUnavailable, "verification email not sent: SMTP is not configured ([notify] host/enabled)")
		return
	}
	if a.baseDomain == "" {
		writeError(w, http.StatusServiceUnavailable, "verification email not sent: no sender domain configured")
		return
	}
	plain, err := a.store.CreateEmailVerification(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create verification token: "+err.Error())
		return
	}
	// Dedicated per-send service: registering the platform identity here must
	// not clobber a customer identity registered on the shared cached service.
	svc := email.NewService(a.mailer, 60)
	if err := svc.Register(email.DomainIdentity{Domain: a.baseDomain, FromName: "Porter", Enabled: true}); err != nil {
		writeError(w, http.StatusInternalServerError, "sender identity: "+err.Error())
		return
	}
	msg := email.Message{
		Domain:  a.baseDomain,
		To:      []string{u.Email},
		Subject: "Verify your Porter email",
		Text:    "Your email verification token (valid 24h):\n\n" + plain + "\n\nPOST it to /auth/verify-email as {\"token\": \"...\"}.",
	}
	if err := svc.Send(msg); err != nil {
		writeError(w, http.StatusBadGateway, "verification email failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "email": u.Email})
}

// recovered from internal/api/feature_audit.go
func (a *API) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userID, err := a.store.ConsumeEmailVerification(req.Token)
	if err != nil {
		// Failed verification attempts are exactly what a security audit wants
		// to see (SRS §26); never include the submitted token in the record.
		_ = a.store.AppendAuditRecord("", "", "auth.verify_email", r.URL.Path,
			currentRequestID(r), remoteIP(r.RemoteAddr), "denied")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.store.MarkUserEmailVerified(userID); err != nil {
		writeError(w, http.StatusInternalServerError, "mark email verified: "+err.Error())
		return
	}
	_ = a.store.AppendAuditRecord("user", userID, "auth.verify_email", r.URL.Path,
		currentRequestID(r), remoteIP(r.RemoteAddr), "allowed")
	writeJSON(w, http.StatusOK, map[string]any{"status": "verified", "user_id": userID})
}
