// Backup envelope addressing, triggers and retention (OCM-11). Crypto
// (AES-256-CTR+HMAC, key wrap) executes in the agent; the control plane
// tracks descriptors and enforces retention + verify-before-trust.
package storage

import (
	"fmt"
	"sort"
	"time"
)

// Backup triggers.
const (
	TriggerManual    = "manual"
	TriggerSchedule  = "schedule"
	TriggerMigration = "migration"
)

// DefaultRetention keeps the last N verified backups (OCM keeps 3).
const DefaultRetention = 3

// Backup is one backup descriptor (crypto sidecars live in ObjectStore).
type Backup struct {
	ID        string
	Workload  string
	Object    string // backups/{workload}/{ts}.ext4.zst.enc
	SizeBytes int64
	SHA256    string
	Trigger   string
	Verified  bool
	CreatedAt time.Time
}

// ObjectPath renders the canonical backup object path.
func ObjectPath(workloadID string, t time.Time) string {
	return fmt.Sprintf("backups/%s/%s.ext4.zst.enc", workloadID, t.UTC().Format("20060102-150405"))
}

// Retain selects which backups survive retention: newest N verified first,
// then newest unverified to fill. A backup is never trusted until restore is
// verified (SRS §33), so unverified entries only fill empty slots.
func Retain(backups []Backup, n int) (keep, drop []Backup) {
	if n <= 0 {
		n = DefaultRetention
	}
	cp := append([]Backup(nil), backups...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].CreatedAt.After(cp[j].CreatedAt) })
	verified, unverified := []Backup{}, []Backup{}
	for _, b := range cp {
		if b.Verified {
			verified = append(verified, b)
		} else {
			unverified = append(unverified, b)
		}
	}
	keep = append(keep, verified...)
	for _, b := range unverified {
		if len(keep) >= n {
			break
		}
		keep = append(keep, b)
	}
	if len(keep) > n {
		keep = keep[:n]
	}
	kept := map[string]bool{}
	for _, b := range keep {
		kept[b.ID] = true
	}
	for _, b := range cp {
		if !kept[b.ID] {
			drop = append(drop, b)
		}
	}
	return keep, drop
}
