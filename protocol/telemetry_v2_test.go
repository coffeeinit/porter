package protocol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func validTelemetry() TelemetryBatchV2 {
	now := time.Now().UTC()
	return TelemetryBatchV2{ProtocolVersion: VersionV2, NodeID: "node_1", Logs: []TelemetryLogV2{{RecordID: "log_1", InstanceID: "vm_1", ReportedAt: now, Level: "info", Message: "started"}}, Metrics: []TelemetryMetricV2{{RecordID: "metric_1", ReportedAt: now, Name: "node.cpu_ticks", Value: 12, Unit: "ticks"}}}
}
func TestTelemetryBatchV2WireAndValidation(t *testing.T) {
	b := validTelemetry()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"protocol_version":"v2"`, `"node_id":"node_1"`, `"logs"`, `"metrics"`, `"record_id"`, `"reported_at"`, `"instance_id":"vm_1"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s in %s", field, data)
		}
	}
	if strings.Contains(string(data), `"labels"`) || strings.Contains(string(data), `"attributes"`) {
		t.Fatalf("unexpected arbitrary fields: %s", data)
	}
}
func TestTelemetryBatchV2RejectsInvalidRecords(t *testing.T) {
	base := validTelemetry()
	cases := []TelemetryBatchV2{}
	bad := validTelemetry()
	bad.Logs[0].Level = "trace"
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Logs[0].Message = strings.Repeat("x", 4097)
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Metrics[0].Name = "../oops"
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Metrics[0].Value = math.Inf(1)
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Logs[0].RecordID = "../unsafe"
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Metrics[0].RecordID = bad.Logs[0].RecordID
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Logs[0].ReportedAt = time.Now().Add(-31 * 24 * time.Hour)
	cases = append(cases, bad)
	bad = validTelemetry()
	bad.Logs = make([]TelemetryLogV2, 257)
	for i := range bad.Logs {
		bad.Logs[i] = TelemetryLogV2{RecordID: "l" + strings.Repeat("a", i%2), ReportedAt: time.Now(), Level: "info"}
	}
	cases = append(cases, bad)
	for i, b := range cases {
		if err := b.Validate(); err == nil {
			t.Errorf("invalid case %d accepted", i)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
}
