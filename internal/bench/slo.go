// Boot SLOs + sizing ladder (PVE-24): staged boot-time budgets measured on
// worst-case hardware (Atom floor, not the dev i7) plus the RAM ladder that
// guides profile defaults. `porter bench` asserts against this table; the
// doctor reports drift without failing the gate.
package bench

import (
	"fmt"
	"time"
)

// BootSLO is one staged budget: start -> serial -> shell -> agent.
type BootSLO struct {
	Image                string
	Serial, Shell, Agent time.Duration
}

// StagedSLOs are budgets by image class (worst-case floor hardware).
var StagedSLOs = []BootSLO{
	{Image: "alpine", Serial: 500 * time.Millisecond, Shell: 4 * time.Second, Agent: 10 * time.Second},
	{Image: "debian", Serial: 10 * time.Second, Shell: 20 * time.Second, Agent: 45 * time.Second},
	{Image: "opnsense", Serial: 60 * time.Second, Shell: 90 * time.Second, Agent: 120 * time.Second},
}

// SLOFor returns the budget for an image class, or false when unknown.
func SLOFor(image string) (BootSLO, bool) {
	for _, s := range StagedSLOs {
		if s.Image == image {
			return s, true
		}
	}
	return BootSLO{}, false
}

// RamRung is one sizing-ladder step.
type RamRung struct {
	Class  string // alpine | systemd | docker
	MinMiB int
}

// RamLadder guides memory defaults (PVE-24 ladder: 128/256/512+).
var RamLadder = []RamRung{
	{Class: "alpine", MinMiB: 128},
	{Class: "systemd", MinMiB: 256},
	{Class: "docker", MinMiB: 512},
}

// MinRAM returns the floor for a class.
func MinRAM(class string) (int, error) {
	for _, r := range RamLadder {
		if r.Class == class {
			return r.MinMiB, nil
		}
	}
	return 0, fmt.Errorf("bench: unknown ram class %q", class)
}
