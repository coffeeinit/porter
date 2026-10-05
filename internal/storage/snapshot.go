// Snapshot lifecycle model (FCM-07): pause → create → resume file naming,
// type selection, state machine, and the checksummed export manifest.
// Snapshots persist to ObjectStore (never local sidecar files); this package
// owns naming, validation and transitions only.
package storage

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snapshot types. Full is the default; Diff is for teleport-class flows.
const (
	SnapshotFull = "Full"
	SnapshotDiff = "Diff"
)

// tsLayout matches snapshot-<id>-{vmstate,memfile}-<TS>.fc (FCM-07).
const tsLayout = "20060102-150405"

var snapshotFileRe = regexp.MustCompile(`^snapshot-(.+)-(vmstate|memfile)-(\d{8}-\d{6})\.fc$`)

// FileNames returns the deterministic vmstate + memfile names for a VM at t.
func FileNames(vmID string, t time.Time) (state, mem string) {
	ts := t.UTC().Format(tsLayout)
	safe := strings.ReplaceAll(vmID, "/", "-")
	return fmt.Sprintf("snapshot-%s-vmstate-%s.fc", safe, ts),
		fmt.Sprintf("snapshot-%s-memfile-%s.fc", safe, ts)
}

// Snapshot is one parsed snapshot pair.
type Snapshot struct {
	VMID      string
	Timestamp time.Time
	StateFile string
	MemFile   string
}

// ParseSnapshotPair matches one vmstate+memfile pair from a file listing.
// Unpaired files are ignored (never error: the store may hold partials).
func ParseSnapshotPair(files []string) []Snapshot {
	byKey := map[string]*Snapshot{}
	for _, f := range files {
		m := snapshotFileRe.FindStringSubmatch(path.Base(f))
		if m == nil {
			continue
		}
		ts, err := time.Parse(tsLayout, m[3])
		if err != nil {
			continue
		}
		key := m[1] + "|" + m[3]
		s, ok := byKey[key]
		if !ok {
			s = &Snapshot{VMID: m[1], Timestamp: ts}
			byKey[key] = s
		}
		if m[2] == "vmstate" {
			s.StateFile = f
		} else {
			s.MemFile = f
		}
	}
	out := []Snapshot{}
	for _, s := range byKey {
		if s.StateFile != "" && s.MemFile != "" {
			out = append(out, *s)
		}
	}
	// Newest first, matching the FCM list convention.
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// CreatePhase is the pause → snapshot → resume state machine (FCM-07). The
// resume step runs even on snapshot failure; encode that as an explicit
// transition so controllers cannot strand a VM paused.
type CreatePhase string

const (
	PhasePausing     CreatePhase = "pausing"
	PhaseSnapshotted CreatePhase = "snapshotted"
	PhaseResuming    CreatePhase = "resuming"
	PhaseDone        CreatePhase = "done"
)

// NextCreatePhase advances the snapshot state machine. snapshotErr carries
// the PUT /snapshot/create result; resume always follows, success or not.
func NextCreatePhase(cur CreatePhase, snapshotErr error) (CreatePhase, error) {
	switch cur {
	case PhasePausing:
		return PhaseSnapshotted, nil
	case PhaseSnapshotted:
		return PhaseResuming, snapshotErr
	case PhaseResuming:
		return PhaseDone, nil
	default:
		return "", fmt.Errorf("storage: bad snapshot phase %q", cur)
	}
}

// Manifest is the versioned, checksummed snapshot/export descriptor
// (FCM-09). SHA-256 only; MD5 is rejected.
type Manifest struct {
	APIVersion string            `json:"apiVersion"`
	VMName     string            `json:"vmName"`
	VCPUs      int               `json:"vcpus"`
	MemoryMiB  int               `json:"memoryMiB"`
	Checksums  map[string]string `json:"checksums"` // object path -> hex sha256
}

// Validate enforces manifest hygiene before import.
func (m Manifest) Validate() error {
	if m.APIVersion == "" || m.VMName == "" {
		return fmt.Errorf("storage: manifest needs apiVersion and vmName")
	}
	if len(m.Checksums) == 0 {
		return fmt.Errorf("storage: manifest needs at least one checksum")
	}
	for p, sum := range m.Checksums {
		if len(sum) != 64 {
			return fmt.Errorf("storage: checksum for %s is not sha256 hex", p)
		}
	}
	return nil
}
