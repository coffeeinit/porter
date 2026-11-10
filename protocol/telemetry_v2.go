package protocol

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var (
	telemetryIDRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	telemetryNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,127}$`)
	telemetryUnitRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9./_%:-]{0,31}$`)
)

// TelemetryLogV2 is a single bounded, instance-optional node log event.
type TelemetryLogV2 struct {
	RecordID   string    `json:"record_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	ReportedAt time.Time `json:"reported_at"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
}

// TelemetryMetricV2 is a numeric measurement with an optional unit.
type TelemetryMetricV2 struct {
	RecordID   string    `json:"record_id"`
	InstanceID string    `json:"instance_id,omitempty"`
	ReportedAt time.Time `json:"reported_at"`
	Name       string    `json:"name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit,omitempty"`
}

// TelemetryBatchV2 carries bounded logs and metrics from one authenticated node.
type TelemetryBatchV2 struct {
	ProtocolVersion string              `json:"protocol_version"`
	NodeID          string              `json:"node_id"`
	Logs            []TelemetryLogV2    `json:"logs"`
	Metrics         []TelemetryMetricV2 `json:"metrics"`
}

func (b TelemetryBatchV2) Validate() error {
	return b.ValidateAt(time.Now().UTC())
}

func (b TelemetryBatchV2) ValidateAt(now time.Time) error {
	if b.ProtocolVersion != VersionV2 || !telemetryIDRE.MatchString(b.NodeID) || strings.TrimSpace(b.NodeID) != b.NodeID {
		return fmt.Errorf("telemetry batch requires v2 and a safe node identity")
	}
	if now.IsZero() {
		return fmt.Errorf("telemetry validation time is required")
	}
	if len(b.Logs)+len(b.Metrics) > 256 {
		return fmt.Errorf("telemetry batch exceeds 256 records")
	}
	seen := make(map[string]bool, len(b.Logs)+len(b.Metrics))
	validTime := func(t time.Time) bool {
		now = now.UTC()
		return !t.IsZero() && !t.Before(now.Add(-30*24*time.Hour)) && !t.After(now.Add(time.Minute))
	}
	validIdentity := func(id, instance string, at time.Time) bool {
		if !telemetryIDRE.MatchString(id) || seen[id] || (instance != "" && !telemetryIDRE.MatchString(instance)) || !validTime(at) {
			return false
		}
		seen[id] = true
		return true
	}
	for _, l := range b.Logs {
		if !validIdentity(l.RecordID, l.InstanceID, l.ReportedAt) || (l.Level != "debug" && l.Level != "info" && l.Level != "warn" && l.Level != "error") || len(l.Message) > 4096 {
			return fmt.Errorf("telemetry batch contains an invalid log record")
		}
	}
	for _, m := range b.Metrics {
		if !validIdentity(m.RecordID, m.InstanceID, m.ReportedAt) || !telemetryNameRE.MatchString(m.Name) || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) || (m.Unit != "" && !telemetryUnitRE.MatchString(m.Unit)) {
			return fmt.Errorf("telemetry batch contains an invalid metric record")
		}
	}
	return nil
}
