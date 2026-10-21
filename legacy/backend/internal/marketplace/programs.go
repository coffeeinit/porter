// Provider/model catalogs, invites, and activity log (OCM-23): the
// marketplace substrate beyond installable items — typed credentials,
// model+provider catalogs for the AI gateway, team invitations, and the
// append-only activity feed that powers audit-adjacent UX.
package marketplace

import "fmt"

// Credential is a typed third-party credential (keys validated before
// store, resolved server-side at call time — never shipped to clients).
type Credential struct {
	ID       string
	Kind     string // api-key | oauth | token
	Provider string // validated via provider probe before store
	Ref      string // secret-store reference, never the value
}

// Validate gates credentials: no raw values, provider required.
func (c Credential) Validate() error {
	if c.ID == "" || c.Provider == "" || c.Ref == "" {
		return fmt.Errorf("marketplace: credential needs id, provider and ref")
	}
	switch c.Kind {
	case "api-key", "oauth", "token":
	default:
		return fmt.Errorf("marketplace: unknown credential kind %q", c.Kind)
	}
	return nil
}

// ProviderEntry is one AI-gateway provider (allowlisted custom providers).
type ProviderEntry struct {
	Name    string
	BaseURL string
	Enabled bool
}

// ModelEntry is one model in the catalog with its cost basis.
type ModelEntry struct {
	Provider string
	Model    string
	// Microcents per 1k tokens in/out; zero = untracked.
	InMicrocents  int64
	OutMicrocents int64
}

// Invite is one team invitation (single-use, expiring — enforced by store).
type Invite struct {
	ID    string
	Email string
	Role  string
	Scope string
}

// Validate gates invites.
func (in Invite) Validate() error {
	if in.ID == "" || in.Email == "" || in.Role == "" || in.Scope == "" {
		return fmt.Errorf("marketplace: invite needs id, email, role and scope")
	}
	return nil
}

// ActivityEvent is one append-only feed entry (actor, action, resource).
type ActivityEvent struct {
	Actor      string
	Action     string
	Resource   string
	OccurredAt int64 // unix seconds
}

// Validate gates feed entries.
func (e ActivityEvent) Validate() error {
	if e.Actor == "" || e.Action == "" || e.Resource == "" {
		return fmt.Errorf("marketplace: activity needs actor, action and resource")
	}
	return nil
}
