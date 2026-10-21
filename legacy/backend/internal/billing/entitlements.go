// Entitlement admission (SRS §46): plan_limits keys double as entitlement
// codes that gate runtime admission. The convention deliberately mirrors
// store.OverQuota (this package cannot import store — store imports billing —
// so the semantics are pinned by matching tests in both packages): a missing
// key is unlimited (pass open), an explicit 0 is a real limit of zero.
package billing

// Entitlement codes admitted on (SRS §46 example set as plan_limits keys).
const (
	EntMaxProjects  = "max_projects"
	EntMaxVMs       = "max_vms"
	EntMaxVCPU      = "max_vcpu"
	EntMaxMemoryMiB = "max_memory_mib"
	EntMaxDomains   = "max_domains"
)

// admittedEntitlements is the set CheckEntitlement will judge; codes outside
// it return ok=false so callers pass them open rather than guess.
var admittedEntitlements = map[string]bool{
	EntMaxProjects: true, EntMaxVMs: true, EntMaxVCPU: true,
	EntMaxMemoryMiB: true, EntMaxDomains: true,
}

// CheckEntitlement reports whether would-be usage — current + requested,
// summed by the caller across replicas and resources — exceeds the plan
// limit for code. Convention (mirrors store.OverQuota exactly):
//
//   - key absent   → never over, limit 0 (plans without rows are open);
//   - key present  → over = current+requested > limit, equal fits; an
//     explicit 0 therefore admits only zero usage;
//   - unknown code → ok=false, never over.
func CheckEntitlement(limits map[string]int64, code string, current, requested int64) (over bool, limit int64, ok bool) {
	if !admittedEntitlements[code] {
		return false, 0, false
	}
	lim, has := limits[code]
	if !has {
		return false, 0, true
	}
	return current+requested > lim, lim, true
}
