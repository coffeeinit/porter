// Package types — admin/ops surface resources (2026-10 API consolidation).
//
// These are the serialization contracts for the store's JSONB-backed generic
// CRUD (internal/store/crud_json.go). Every struct carries an ID, a tenant
// OrgID where applicable, and CreatedAt; the whole struct is persisted as one
// JSONB `data` column so schema evolution is additive (new optional fields).
//
// Secret-bearing fields (Token, Key) are never serialized back to clients or
// stored in plaintext: the API layer encrypts into the *Encrypted fields via
// secretbox before Put, and json:"-" keeps the plaintext out of responses.
package types

import "time"

// AIAgent is a governed AI operator identity (SRS §63): same API, same RBAC,
// no superuser path.
type AIAgent struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Model       string    `json:"model,omitempty"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// AIPlan is a proposed AI operation awaiting approval (SRS §63 safety
// classes: proposed → approved/rejected → executed).
type AIPlan struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id,omitempty"`
	Intent        string    `json:"intent"`
	ProposedState string    `json:"proposed_state"`
	Status        string    `json:"status,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// BackupPolicy schedules immutable backups for a project or volume.
type BackupPolicy struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Schedule  string    `json:"schedule,omitempty"`
	Retention int       `json:"retention,omitempty"`
	Enabled   bool      `json:"enabled,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Certificate is a TLS certificate for a domain (imported, or ACME-issued).
type Certificate struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Domain    string    `json:"domain"`
	Status    string    `json:"status,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// CloudInitScript is a cloud-init user-data template for node provisioning.
type CloudInitScript struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// CloudToken stores a provider API token. Token is plaintext in memory only
// (json:"-"); TokenEncrypted is what persists.
type CloudToken struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider,omitempty"`
	Token          string    `json:"-"`
	TokenEncrypted []byte    `json:"token_encrypted,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Credit is a billing credit applied to an org's balance (cents).
type Credit struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	AmountCents int64     `json:"amount_cents"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Customer is a billing customer, optionally owned by a reseller.
type Customer struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id,omitempty"`
	Name       string    `json:"name"`
	Email      string    `json:"email,omitempty"`
	ResellerID string    `json:"reseller_id,omitempty"`
	Status     string    `json:"status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// DNSZone is an authoritative DNS zone managed by Porter.
type DNSZone struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Verified  bool      `json:"verified,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Destination is a backup destination (ObjectStore reference, optionally
// scoped to one server).
type Destination struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"`
	URL       string    `json:"url,omitempty"`
	ServerID  string    `json:"server_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DunningPolicy drives failed-payment retry steps.
type DunningPolicy struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	Name      string    `json:"name"`
	Steps     []string  `json:"steps,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Entitlement is one capability/limit granted by a plan.
type Entitlement struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	PlanID    string    `json:"plan_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Extension is an installed out-of-process extension (SRS §64).
type Extension struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Enabled     bool      `json:"enabled,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

// Gateway is a traffic entrypoint (HTTP/S, TCP, WS) terminating TLS.
type Gateway struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// GatewayRoute binds a hostname/path on a gateway to a backend service.
type GatewayRoute struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	GatewayID string    `json:"gateway_id"`
	Hostname  string    `json:"hostname,omitempty"`
	Path      string    `json:"path,omitempty"`
	Backend   string    `json:"backend,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// IntegrationToken is a scoped token for an external integration. Token is
// returned once at creation; never listed back.
type IntegrationToken struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	Token     string    `json:"token,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// K8sCluster is an adopted Kubernetes cluster (extension surface, SRS §40:
// k8s is an extension, never the core control plane).
type K8sCluster struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	State     string    `json:"state,omitempty"`
	Version   string    `json:"version,omitempty"`
	Endpoint  string    `json:"endpoint,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// LoadBalancer balances gateway traffic across backends.
type LoadBalancer struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Algorithm string    `json:"algorithm,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// MaintenanceWindow is a time range in which disruptive ops are allowed.
type MaintenanceWindow struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	StartAt   time.Time `json:"start_at,omitempty"`
	EndAt     time.Time `json:"end_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// NodePool is a group of schedulable nodes within a region/zone.
type NodePool struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	RegionID  string    `json:"region_id,omitempty"`
	Nodes     []string  `json:"nodes,omitempty"`
	MinNodes  int       `json:"min_nodes,omitempty"`
	MaxNodes  int       `json:"max_nodes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ObjectStore is an S3-compatible object storage endpoint (SRS §52).
type ObjectStore struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Bucket    string    `json:"bucket,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PaymentMethod is a stored payment instrument for an org.
type PaymentMethod struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Type      string    `json:"type,omitempty"`
	Last4     string    `json:"last4,omitempty"`
	Token     string    `json:"-"`
	Default   bool      `json:"default,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PortForward publishes a host port to a replica target port.
type PortForward struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	ReplicaID  string    `json:"replica_id,omitempty"`
	ListenPort int       `json:"listen_port,omitempty"`
	TargetPort int       `json:"target_port"`
	Protocol   string    `json:"protocol,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Price is one chargeable meter price on a plan.
type Price struct {
	ID              string    `json:"id"`
	OrgID           string    `json:"org_id,omitempty"`
	PlanID          string    `json:"plan_id"`
	Meter           string    `json:"meter,omitempty"`
	UnitAmountCents int64     `json:"unit_amount_cents,omitempty"`
	Currency        string    `json:"currency,omitempty"`
	Interval        string    `json:"interval,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// PrivateKey is an SSH/deploy private key. Key is plaintext in memory only;
// KeyEncrypted is what persists.
type PrivateKey struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id"`
	Name         string    `json:"name"`
	Key          string    `json:"-"`
	KeyEncrypted []byte    `json:"key_encrypted,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Product is a sellable product owning plans and prices.
type Product struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Provider is an infrastructure provider owning regions.
type Provider struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Region is a provider region owning zones.
type Region struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id,omitempty"`
	Name       string    `json:"name"`
	ProviderID string    `json:"provider_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Registry is a container registry credential for image pulls.
type Registry struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Host      string    `json:"host"`
	Username  string    `json:"username,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Reseller sells Porter capacity to customer orgs.
type Reseller struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	Name      string    `json:"name"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// S3Storage is an S3-compatible storage account used by backup destinations.
type S3Storage struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Bucket    string    `json:"bucket,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SLO is a service-level objective with a target and window.
type SLO struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Metric    string    `json:"metric,omitempty"`
	Target    string    `json:"target,omitempty"`
	Window    string    `json:"window,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ServiceAccount is a non-human principal owning API keys.
type ServiceAccount struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ServiceAccountKey is one hashed token belonging to a service account.
// Only the hash persists; the raw token is shown once at creation.
type ServiceAccountKey struct {
	ID               string    `json:"id"`
	ServiceAccountID string    `json:"service_account_id"`
	TokenHash        string    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
}

// Silence suppresses alert notifications for a scope until a time.
type Silence struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	AlertID   string    `json:"alert_id,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Until     time.Time `json:"until,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Storage is a storage pool assignment for volumes.
type Storage struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// StorageClass names a volume provisioning class.
type StorageClass struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id,omitempty"`
	Name        string    `json:"name"`
	Provisioner string    `json:"provisioner,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SyntheticCheck is a scheduled probe (DNS/TLS/HTTP/TCP) against a target.
type SyntheticCheck struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	Name       string    `json:"name"`
	Target     string    `json:"target"`
	Type       string    `json:"type,omitempty"`
	IntervalS  int       `json:"interval_s,omitempty"`
	LastStatus string    `json:"last_status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// TaxRate is a tax percentage applied at invoice time.
type TaxRate struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	Name      string    `json:"name"`
	Percent   float64   `json:"percent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Webhook is an outbound webhook endpoint registered by an org.
type Webhook struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events,omitempty"`
	Secret    string    `json:"-"`
	Active    bool      `json:"active,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Workflow is a durable Trigger/Condition/Action automation (SRS §61).
type Workflow struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Trigger   string    `json:"trigger,omitempty"`
	Enabled   bool      `json:"enabled,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Zone is a provider availability zone within a region.
type Zone struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id,omitempty"`
	Name      string    `json:"name"`
	RegionID  string    `json:"region_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
