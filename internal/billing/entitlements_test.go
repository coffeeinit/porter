// Entitlement admission tests: table-driven over the OverQuota conventions
// (missing key = unlimited, explicit 0 = a real limit of zero, equal-to-limit
// fits, over returns the limit) plus current+requested summation and the
// unknown-code gate. Pure — no DB, no network.
package billing

import "testing"

func TestWP2CheckEntitlementSemantics(t *testing.T) {
	cases := []struct {
		name      string
		limits    map[string]int64
		code      string
		current   int64
		requested int64
		wantOver  bool
		wantLimit int64
		wantOK    bool
	}{
		// Missing key = unlimited (quotas pass open, never a silent deny).
		{"absent key passes open", map[string]int64{}, EntMaxVCPU, 0, 1 << 30, false, 0, true},
		{"nil limits pass open", nil, EntMaxMemoryMiB, 0, 1 << 30, false, 0, true},
		{"unrelated key is independent", map[string]int64{EntMaxVMs: 3}, EntMaxDomains, 0, 100, false, 0, true},
		// Under and equal-to-limit fit: the argument is would-be usage.
		{"under limit fits", map[string]int64{EntMaxVCPU: 8}, EntMaxVCPU, 2, 4, false, 8, true},
		{"equal to limit fits", map[string]int64{EntMaxVCPU: 6}, EntMaxVCPU, 4, 2, false, 6, true},
		{"equal to memory limit fits", map[string]int64{EntMaxMemoryMiB: 512}, EntMaxMemoryMiB, 0, 512, false, 512, true},
		// Over returns the limit so callers can name it in the 403.
		{"over by one", map[string]int64{EntMaxMemoryMiB: 512}, EntMaxMemoryMiB, 0, 513, true, 512, true},
		{"current plus requested sums over", map[string]int64{EntMaxVCPU: 8}, EntMaxVCPU, 6, 3, true, 8, true},
		// Explicit 0 is a real limit of zero, NOT unlimited.
		{"explicit zero blocks positive", map[string]int64{EntMaxDomains: 0}, EntMaxDomains, 0, 1, true, 0, true},
		{"explicit zero admits zero", map[string]int64{EntMaxDomains: 0}, EntMaxDomains, 0, 0, false, 0, true},
		// Zero usage always fits a positive limit.
		{"zero requested fits", map[string]int64{EntMaxVCPU: 4}, EntMaxVCPU, 0, 0, false, 4, true},
	}
	for _, tc := range cases {
		over, limit, ok := CheckEntitlement(tc.limits, tc.code, tc.current, tc.requested)
		if over != tc.wantOver || limit != tc.wantLimit || ok != tc.wantOK {
			t.Errorf("%s: CheckEntitlement(%v, %q, %d, %d) = (over=%v, limit=%d, ok=%v), want (over=%v, limit=%d, ok=%v)",
				tc.name, tc.limits, tc.code, tc.current, tc.requested,
				over, limit, ok, tc.wantOver, tc.wantLimit, tc.wantOK)
		}
	}
}

func TestWP2CheckEntitlementKnownCodes(t *testing.T) {
	// Every code in the SRS §46 admission set must be judged, not passed
	// through as unknown.
	for _, code := range []string{EntMaxProjects, EntMaxVMs, EntMaxVCPU, EntMaxMemoryMiB, EntMaxDomains} {
		if _, _, ok := CheckEntitlement(map[string]int64{code: 1}, code, 1, 1); !ok {
			t.Errorf("admission code %q must be supported", code)
		}
	}
}

func TestWP2CheckEntitlementUnknownCode(t *testing.T) {
	// Codes outside the admission set report ok=false and never over —
	// callers pass them open rather than guess at semantics.
	over, limit, ok := CheckEntitlement(map[string]int64{"custom_flag": 1}, "custom_flag", 0, 1)
	if over || limit != 0 || ok {
		t.Fatalf("unknown code must be (false, 0, false), got (over=%v, limit=%d, ok=%v)", over, limit, ok)
	}
}
