// Package resource defines the canonical Porter domain types.
package resource

// Org is an organization that groups projects and users.
type Org struct {
	Metadata `json:"metadata"`
	Spec     OrgSpec   `json:"spec"`
	Status   OrgStatus `json:"status"`
}

func (o *Org) GetMetadata() *Metadata { return &o.Metadata }
func (o *Org) GetSpec() interface{}   { return o.Spec }
func (o *Org) GetStatus() *Status     { return &o.Status.Status }
func (o *Org) SetStatus(s *Status)    { o.Status.Status = *s }
func (o *Org) GetKind() string        { return KindOrg }

// OrgSpec is the desired state of an org
type OrgSpec struct {
	// Name is the org name
	Name string `json:"name"`

	// OwnerID is the user ID of the owner
	OwnerID string `json:"owner_id,omitempty"`

	// IsDefault indicates if this is the default org
	IsDefault bool `json:"is_default,omitempty"`

	// Description of the org
	Description string `json:"description,omitempty"`

	// BillingEmail for invoices
	BillingEmail string `json:"billing_email,omitempty"`

	// Settings org-level settings
	Settings OrgSettings `json:"settings,omitempty"`
}

// OrgSettings are org-level settings
type OrgSettings struct {
	// DefaultProjectQuota default project quota
	DefaultProjectQuota int `json:"default_project_quota,omitempty"`

	// MaxProjects max projects
	MaxProjects int `json:"max_projects,omitempty"`

	// AllowedRegions regions allowed for this org
	AllowedRegions []string `json:"allowed_regions,omitempty"`

	// RequireMFA requires MFA for org members
	RequireMFA bool `json:"require_mfa,omitempty"`
}

// OrgStatus is the observed state of an org
type OrgStatus struct {
	Status `json:",inline"`

	// MemberCount is the number of members
	MemberCount int `json:"member_count,omitempty"`

	// ProjectCount is the number of projects
	ProjectCount int `json:"project_count,omitempty"`

	// TeamCount is the number of teams
	TeamCount int `json:"team_count,omitempty"`
}

// Team is a team within an org
type Team struct {
	Metadata `json:"metadata"`
	Spec     TeamSpec   `json:"spec"`
	Status   TeamStatus `json:"status"`
}

func (t *Team) GetMetadata() *Metadata { return &t.Metadata }
func (t *Team) GetSpec() interface{}   { return t.Spec }
func (t *Team) GetStatus() *Status     { return &t.Status.Status }
func (t *Team) SetStatus(s *Status)    { t.Status.Status = *s }
func (t *Team) GetKind() string        { return KindTeam }

// TeamSpec is the desired state of a team
type TeamSpec struct {
	// OrgID is the org ID
	OrgID string `json:"org_id"`

	// Name is the team name
	Name string `json:"name"`

	// Description of the team
	Description string `json:"description,omitempty"`

	// Members is the list of user IDs
	Members []string `json:"members,omitempty"`

	// Roles is the team roles
	Roles []string `json:"roles,omitempty"`
}

// TeamStatus is the observed state of a team
type TeamStatus struct {
	Status `json:",inline"`

	// MemberCount is the number of members
	MemberCount int `json:"member_count,omitempty"`
}
