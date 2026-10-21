// Admission chain and effective-capacity policy (SRS §11, OCM-04). Every
// create/update flows Request → AuthN → AuthZ → Quota → Policy → Validate →
// Defaults → Persist; this package models the policy/decision half. RBAC
// enforcement stays in rbac; quota counters stay in store; the formula and
// precedence live here so admission and scheduler share one policy.
package policy

import "fmt"

// Stage names the admission pipeline step (SRS §11).
const (
	StageAuthN      = "authentication"
	StageAuthZ      = "authorization"
	StageQuota      = "quota"
	StagePolicy     = "policy"
	StageValidate   = "validation"
	StageDefaulting = "defaulting"
)

// Decision is one stage verdict.
type Decision struct {
	Allow  bool
	Stage  string
	Reason string
}

// Deny builds a deny verdict.
func Deny(stage, reason string) Decision {
	return Decision{Allow: false, Stage: stage, Reason: reason}
}

// Allow builds an allow verdict.
func Allow(stage string) Decision { return Decision{Allow: true, Stage: stage} }

// Evaluate folds stage verdicts: first deny wins, full allow otherwise.
func Evaluate(stages []Decision) Decision {
	for _, d := range stages {
		if !d.Allow {
			return d
		}
	}
	return Allow("admission")
}

// Scope ranks capacity-policy specificity (OCM-04): host > pool >
// provider > global. Lower value = more specific.
type Scope int

const (
	ScopeHost Scope = iota
	ScopePool
	ScopeProvider
	ScopeGlobal
)

// CapacityPolicy bounds one scope. Nil pointers mean "inherit".
type CapacityPolicy struct {
	Scope         Scope
	CPUOvercommit *float64
	MemOvercommit *float64
	ReserveVCPU   *int
	ReserveMemMiB *int
	Enabled       *bool
}

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }
func bptr(b bool) *bool       { return &b }

// Defaults returns the bare-metal tuned baseline (OCM-04).
func Defaults() CapacityPolicy {
	return CapacityPolicy{
		Scope:         ScopeGlobal,
		CPUOvercommit: fptr(2.0),
		MemOvercommit: fptr(1.0),
		ReserveVCPU:   iptr(1),
		ReserveMemMiB: iptr(2048),
		Enabled:       bptr(true),
	}
}

// Merge overlays specific → general: the most specific set field wins.
func Merge(policies ...CapacityPolicy) CapacityPolicy {
	out := Defaults()
	// Apply general first so specific overrides stick.
	for i := len(policies) - 1; i >= 0; i-- {
		p := policies[i]
		if p.CPUOvercommit != nil {
			out.CPUOvercommit = p.CPUOvercommit
		}
		if p.MemOvercommit != nil {
			out.MemOvercommit = p.MemOvercommit
		}
		if p.ReserveVCPU != nil {
			out.ReserveVCPU = p.ReserveVCPU
		}
		if p.ReserveMemMiB != nil {
			out.ReserveMemMiB = p.ReserveMemMiB
		}
		if p.Enabled != nil {
			out.Enabled = p.Enabled
		}
	}
	return out
}

// EffectiveCapacity applies the OCM formula: effective = floor((phys -
// reserve) * overcommit). Disk never overcommits (not modeled here).
func EffectiveCapacity(physVCPU, physMemMiB int, pol CapacityPolicy) (vcpu, mem int, err error) {
	if pol.CPUOvercommit == nil || pol.MemOvercommit == nil || pol.ReserveVCPU == nil || pol.ReserveMemMiB == nil {
		return 0, 0, fmt.Errorf("policy: capacity policy has unset fields (merge with Defaults first)")
	}
	if *pol.CPUOvercommit < 1.0 || *pol.MemOvercommit < 1.0 {
		return 0, 0, fmt.Errorf("policy: overcommit below 1.0")
	}
	vcpu = int(float64(physVCPU-*pol.ReserveVCPU) * *pol.CPUOvercommit)
	mem = int(float64(physMemMiB-*pol.ReserveMemMiB) * *pol.MemOvercommit)
	if vcpu < 0 {
		vcpu = 0
	}
	if mem < 0 {
		mem = 0
	}
	return vcpu, mem, nil
}
