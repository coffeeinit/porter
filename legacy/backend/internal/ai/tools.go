// Governed AI-operator surface (SRS §44, OCM-13/14): the typed tool catalog
// with safety classes, plus the change-plan shape non-trivial AI operations
// must produce. AI uses the same API/RBAC/audit as humans; arbitrary shell
// is an explicit privileged capability, never a default.
package ai

import "fmt"

// Safety classes (README safety model).
const (
	RiskRead        = "READ"        // automatic
	RiskLow         = "LOW_RISK"    // policy controlled
	RiskProd        = "PROD_CHANGE" // policy + approval
	RiskDestructive = "DESTRUCTIVE" // explicit approval by default
)

// Tool is one typed resource operation the agent may invoke.
type Tool struct {
	Name        string // e.g. inspect_resource
	Risk        string // Risk* class
	Description string
}

// Catalog lists the SRS §44 typed tools with their safety classes.
var Catalog = []Tool{
	{"inspect_resource", RiskRead, "read one resource (spec/status/conditions)"},
	{"list_resources", RiskRead, "scoped resource listing"},
	{"get_logs", RiskRead, "workload/control-plane logs"},
	{"query_metrics", RiskRead, "metrics range query"},
	{"query_traces", RiskRead, "trace lookup"},
	{"get_events", RiskRead, "versioned event stream"},
	{"get_topology", RiskRead, "provider to workload traversal"},
	{"get_usage", RiskRead, "usage and cost readout"},
	{"run_diagnostics", RiskLow, "porter doctor subset against a scope"},
	{"create_snapshot", RiskLow, "snapshot a workload volume"},
	{"scale_service", RiskProd, "change replica count"},
	{"restart_workload", RiskProd, "restart within disruption budget"},
	{"create_deployment", RiskProd, "new deployment from an artifact"},
	{"rollback_deployment", RiskProd, "rollback to a known version"},
	{"modify_network", RiskDestructive, "network/policy attachment change"},
	{"manage_domain", RiskDestructive, "domain verify/attach/detach"},
	{"manage_certificate", RiskDestructive, "certificate issue/replace"},
	{"restore_backup", RiskDestructive, "restore over live state"},
}

// Classify returns the safety class for a tool name.
func Classify(name string) (string, error) {
	for _, t := range Catalog {
		if t.Name == name {
			return t.Risk, nil
		}
	}
	return "", fmt.Errorf("ai: unknown tool %q (arbitrary shell is never implicit)", name)
}

// Plan is the change plan non-trivial AI operations must produce (SRS §44)
// before execution: intent, diff, blast radius, cost, approvals, verify and
// rollback. Humans approve PROD+ per policy.
type Plan struct {
	Intent        string
	CurrentState  string
	ProposedState string
	Changes       []string
	Dependencies  []string
	Risk          string
	CostImpact    string
	Permissions   []string
	ApprovalBy    string // empty = not yet approved
	Steps         []string
	Verification  string
	Rollback      string
}

// Validate ensures the plan is reviewable before it can execute.
func (p Plan) Validate() error {
	if p.Intent == "" || len(p.Steps) == 0 || p.Verification == "" || p.Rollback == "" {
		return fmt.Errorf("ai: plan needs intent, steps, verification and rollback")
	}
	switch p.Risk {
	case RiskRead, RiskLow, RiskProd, RiskDestructive:
	default:
		return fmt.Errorf("ai: unknown risk class %q", p.Risk)
	}
	if (p.Risk == RiskProd || p.Risk == RiskDestructive) && p.ApprovalBy == "" {
		return fmt.Errorf("ai: %s plans need an approver", p.Risk)
	}
	return nil
}
