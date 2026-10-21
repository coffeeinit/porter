// Effective-capacity math for the scheduler hot path (OCM-04). Policy
// precedence and defaults live in policy; this file keeps the pure formula
// next to PickNode so scoring never imports the world.
package scheduler

// Effective returns floor((phys - reserve) * overcommit), floored at 0.
// Overcommit below 1.0 is rejected by policy before it reaches here.
func Effective(phys, reserve int, overcommit float64) int {
	eff := int(float64(phys-reserve) * overcommit)
	if eff < 0 {
		return 0
	}
	return eff
}

// Overcommitted reports whether allocated capacity exceeds the effective
// figure. Such nodes go unschedulable but are never evicted (OCM-04).
func Overcommitted(allocated, effective int) bool {
	return allocated > effective
}
