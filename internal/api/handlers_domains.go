// Domains, DNS zones/records, certificates, Cloudflare.
package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"

	"porter/internal/dns"
	"porter/internal/gateway"
	"porter/internal/store"
	"porter/internal/types"
)

// ============================================================================
// Domains / DNS / zones / certificates
// ============================================================================

func (a *API) handleListDomains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListDomains(a.projectID(r)))
}

func (a *API) handleAddDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		Type   string `json:"type"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if req.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	d := &types.Domain{ProjectID: a.projectID(r), Domain: req.Domain, Type: req.Type}
	a.store.AddDomain(a.projectID(r), d)
	if a.hub != nil {
		a.hub.Broadcast("domain.status", map[string]any{"domain": d.Domain, "status": "pending"})
	}
	writeJSON(w, http.StatusCreated, d)
}

func (a *API) handleDomainRecords(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListDNSRecords(a.projectID(r)))
}

func (a *API) handleGetDomain(w http.ResponseWriter, r *http.Request) {
	for _, d := range a.store.ListDomains(a.projectID(r)) {
		if d.Domain == r.PathValue("domainId") {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	writeError(w, http.StatusNotFound, "domain not found")
}

func (a *API) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	if a.store.RemoveDomain(a.projectID(r), r.PathValue("domainId")) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
		return
	}
	writeError(w, http.StatusNotFound, "domain not found")
}

func (a *API) handleVerifyDomain(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domainId")
	resolver := net.Resolver{}
	ips, err := resolver.LookupHost(context.Background(), domain)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"domain": domain, "status": "unverified", "detail": "DNS lookup failed: " + err.Error()})
		return
	}
	detail := "resolves to " + strings.Join(ips, ", ")
	status := "verified"
	if a.baseDomain != "" && (domain == a.baseDomain || strings.HasSuffix(domain, "."+a.baseDomain)) {
		status = "verified"
	} else if len(ips) == 0 {
		status = "unverified"
		detail = "no A/AAAA records"
	}
	writeJSON(w, http.StatusOK, map[string]any{"domain": domain, "status": status, "detail": detail, "records": ips})
}

func (a *API) handleProjectDNS(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListDNSRecords(a.projectID(r)))
}

// Global DNS zones (SRS §29)

func (a *API) handleListDNSZones(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dns/zones")
}

func (a *API) handleCreateDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /dns/zones")
}

func (a *API) handleGetDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dns/zones/{id}")
}

func (a *API) handlePatchDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /dns/zones/{id}")
}

func (a *API) handleDeleteDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /dns/zones/{id}")
}

func (a *API) handleListDNSRecords(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dns/zones/{id}/records")
}

func (a *API) handleCreateDNSRecord(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /dns/zones/{id}/records")
}

func (a *API) handleGetDNSRecord(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /dns/zones/{id}/records/{rid}")
}

func (a *API) handlePatchDNSRecord(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /dns/zones/{id}/records/{rid}")
}

func (a *API) handleDeleteDNSRecord(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /dns/zones/{id}/records/{rid}")
}

func (a *API) handleVerifyDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /dns/zones/{id}/verify")
}

func (a *API) handlePropagateDNSZone(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /dns/zones/{id}/propagate")
}

// Certificates (SRS §30)

func (a *API) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /certificates")
}

func (a *API) handleCreateCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /certificates")
}

func (a *API) handleGetCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /certificates/{id}")
}

func (a *API) handlePatchCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "PATCH /certificates/{id}")
}

func (a *API) handleDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "DELETE /certificates/{id}")
}

func (a *API) handleIssueCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /certificates/{id}/issue")
}

func (a *API) handleRenewCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /certificates/{id}/renew")
}

func (a *API) handleRevokeCertificate(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "POST /certificates/{id}/revoke")
}

func (a *API) handleListCertificateDeployments(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, "GET /certificates/{id}/deployments")
}

func (a *API) handleVMCompatDomains(w http.ResponseWriter, r *http.Request) {
	vm, ok := a.store.GetVM(r.PathValue("replicaId"))
	if !ok {
		writeError(w, http.StatusNotFound, "replica not found")
		return
	}
	writeJSON(w, http.StatusOK, a.store.ListDomains(vm.ProjectID))
}

func (a *API) cfClient(projectID string) (gateway.CFClient, error) {
	token, err := a.projectSecret(projectID, "cf_token")
	if err != nil {
		return gateway.CFClient{}, err
	}
	account, _ := a.projectSecret(projectID, "cf_account")
	return gateway.CFClient{Token: token, Account: account, ProxyURL: os.Getenv("PORTER_EGRESS_PROXY")}, nil
}

func (a *API) handleCFTunnelCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := cf.CreateTunnel(req.Name)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (a *API) handleCFTunnelDelete(w http.ResponseWriter, r *http.Request) {
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.DeleteTunnel(r.PathValue("tunnelId")); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleCFIngress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rules []gateway.IngressRule `json:"rules"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Rules) == 0 {
		writeError(w, http.StatusBadRequest, "rules are required (last must be the catch-all)")
		return
	}
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.PutIngressConfig(r.PathValue("tunnelId"), req.Rules); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ingress replaced"})
}

func (a *API) handleCFDNS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID, Hostname, Target string
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.UpsertCNAME(req.ZoneID, req.Hostname, req.Target); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "upserted"})
}

func (a *API) handleCFDNSA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID   string `json:"zone_id"`
		Hostname string `json:"hostname"`
		IP       string `json:"ip"`
		Proxied  bool   `json:"proxied"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.UpsertA(req.ZoneID, req.Hostname, req.IP, req.Proxied); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "upserted", "proxied": req.Proxied})
}

func (a *API) handleCFDNSTXT(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ZoneID, Hostname, Value string
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.UpsertTXT(req.ZoneID, req.Hostname, req.Value); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "upserted"})
}

func (a *API) handleCFDNSDelete(w http.ResponseWriter, r *http.Request) {
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cf.DeleteDNSRecord(r.PathValue("zoneId"), r.PathValue("recordId")); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (a *API) handleCFZones(w http.ResponseWriter, r *http.Request) {
	cf, err := a.cfClient(a.projectID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	zoneID, err := cf.FindZoneID(r.URL.Query().Get("domain"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"zone_id": zoneID})
}

func (a *API) handleDomainChallenge(w http.ResponseWriter, r *http.Request) {
	tok, err := dns.MintChallengeToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	domain := r.PathValue("domainId")
	writeJSON(w, http.StatusOK, map[string]any{
		"host": dns.ChallengeHost(domain), "type": "TXT", "value": tok,
		"note": "publish this TXT (or POST it via cf/dns/txt with a token), then verify-txt",
	})
}

func (a *API) handleDomainVerifyTXT(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	ok, detail := dns.VerifyTXTChallenge(r.PathValue("domainId"), req.Token)
	writeJSON(w, http.StatusOK, map[string]any{"verified": ok, "detail": detail})
}

func (a *API) handleServerCACertificate(w http.ResponseWriter, r *http.Request) {
	a.settingsGetForServer(w, r, "ca-certificate")
}

// recovered from internal/api/feature_domains.go
func (a *API) handleListOrgDomains(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgIDFromHeader(r)
	domains := a.store.ListOrgDomains(orgID)
	if domains == nil {
		domains = []store.OrgDomainRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains})
}

// recovered from internal/api/feature_domains.go
func (a *API) handleVerifyOrgDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := readJSON(r, &req); err != nil || req.Domain == "" {
		writeError(w, http.StatusBadRequest, "domain is required")
		return
	}
	if a.domainMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "domain manager not configured")
		return
	}
	ok, detail := a.domainMgr.VerifyDomain(&types.Domain{Domain: req.Domain})
	status := "unverified"
	if ok {
		status = "verified"
	}
	writeJSON(w, http.StatusOK, map[string]any{"domain": req.Domain, "status": status, "detail": detail})
}
