// Commercial metering model (OCM-13, SRS §47). Rating/invoicing stay
// deferred per the package contract; this file defines the meter catalog,
// the replayable usage-event shape (idempotency-keyed), and microcent cost
// math shared by the agent reporter and the future rater.
package billing

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Meter codes (SRS §47 initial set).
const (
	MeterVCPUSeconds   = "vcpu_seconds"
	MeterMemMiBSeconds = "mem_mib_seconds"
	MeterStorageGBMo   = "storage_gb_months"
	MeterObjectGBMo    = "object_gb_months"
	MeterNetBytes      = "network_bytes"
	MeterPublicIPHours = "public_ip_hours"
	MeterSnapshots     = "snapshots"
	MeterBackups       = "backups"
	MeterBuildMinutes  = "build_minutes"
	MeterDeployments   = "deployments"
	MeterLogBytes      = "log_bytes"
)

// validMeters gates ingestion: unknown codes are rejected, never stored.
var validMeters = map[string]bool{
	MeterVCPUSeconds: true, MeterMemMiBSeconds: true, MeterStorageGBMo: true,
	MeterObjectGBMo: true, MeterNetBytes: true, MeterPublicIPHours: true,
	MeterSnapshots: true, MeterBackups: true, MeterBuildMinutes: true,
	MeterDeployments: true, MeterLogBytes: true,
}

// UsageEvent is one replayable, deduplicated meter reading (SRS §47).
type UsageEvent struct {
	EventID        string
	CustomerID     string
	OrganizationID string
	SubscriptionID string
	ResourceID     string
	Metric         string
	Quantity       float64
	At             time.Time
	Dimensions     map[string]string
	IdempotencyKey string
}

// Validate enforces event hygiene before persistence.
func (e UsageEvent) Validate() error {
	if !validMeters[e.Metric] {
		return fmt.Errorf("billing: unknown meter %q", e.Metric)
	}
	if e.Quantity < 0 {
		return fmt.Errorf("billing: negative quantity")
	}
	if e.CustomerID == "" || e.ResourceID == "" {
		return fmt.Errorf("billing: customer and resource ids are required")
	}
	if e.IdempotencyKey == "" {
		return fmt.Errorf("billing: idempotency key is required")
	}
	return nil
}

// DedupeKey derives a stable dedupe key when the reporter omits one:
// sha256(subscription|resource|metric|unix-second|quantity).
func DedupeKey(subscription, resource, metric string, at time.Time, qty float64) string {
	raw := fmt.Sprintf("%s|%s|%s|%d|%f", subscription, resource, metric, at.Unix(), qty)
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

// Microcents converts a quantity × unit-price pair to integer microcents
// (1/1e6 of a currency unit), the OCM llm_usage accounting unit.
func Microcents(quantity, unitPrice float64) int64 {
	return int64(quantity * unitPrice * 1e6)
}
