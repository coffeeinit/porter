package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	v1 "porter/controlplane/contract/v1"
	"porter/protocol"
)

const (
	telemetryRetention = 30 * 24 * time.Hour
	maxTelemetryLogsPerNode = 10_000
	maxTelemetryMetricsPerNode = 100_000
	telemetryPruneBatch = 1_000
)

var (
	ErrTelemetryConflict = errors.New("telemetry record ID conflicts with a different payload")
	telemetryBearerRE = regexp.MustCompile(`(?i)(\b(?:authorization\s*:\s*)?bearer\s+)[A-Za-z0-9._~+/-]+=*`)
	telemetrySecretRE = regexp.MustCompile(`(?i)(\b(?:password|passwd|token|secret|api[_-]?key|access[_-]?key)\b\s*[:=]\s*["']?)[^\s,"';&]+`)
	telemetryPathRE = regexp.MustCompile(`(^|[\s"'(=])(/[A-Za-z0-9._+~-]+(?:/[A-Za-z0-9._+~-]+)*)`)
)

// TelemetryFilter is the bounded time/resource scope of a telemetry query.
type TelemetryFilter struct {
	From       time.Time
	To         time.Time
	NodeID     string
	InstanceID string
}

// TelemetryIngestCounts distinguishes newly stored records from idempotent retries.
type TelemetryIngestCounts struct {
	LogsAccepted    int
	MetricsAccepted int
	Duplicates      int
}

// IngestAgentTelemetry stores a validated v2 batch under the authenticated node
// identity. Instance ownership and project/service scope are derived in the DB;
// no caller-provided project, service, or node IDs are trusted for row ownership.
func (s *Store) IngestAgentTelemetry(ctx context.Context, authenticatedNodeID string, batch protocol.TelemetryBatchV2, now time.Time) (TelemetryIngestCounts, error) {
	var counts TelemetryIngestCounts
	if !validTelemetryResourceID(authenticatedNodeID) || batch.NodeID != authenticatedNodeID {
		return counts, fmt.Errorf("%w: telemetry node identity does not match authenticated node", ErrInvalidArgument)
	}
	if now.IsZero() {
		return counts, fmt.Errorf("%w: telemetry receipt time is required", ErrInvalidArgument)
	}
	if err := batch.ValidateAt(now.UTC()); err != nil {
		return counts, fmt.Errorf("%w: invalid telemetry batch", ErrInvalidArgument)
	}
	if len(batch.Logs)+len(batch.Metrics) == 0 {
		return counts, fmt.Errorf("%w: telemetry batch must contain records", ErrInvalidArgument)
	}
	for _, record := range batch.Logs {
		if record.Message == "" || !utf8.ValidString(record.Message) {
			return counts, fmt.Errorf("%w: invalid telemetry log message", ErrInvalidArgument)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return counts, fmt.Errorf("begin telemetry ingestion: %w", err)
	}
	defer tx.Rollback()
	var nodeID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM porter_control_plane.fleet_nodes WHERE id = $1 FOR UPDATE`, authenticatedNodeID).Scan(&nodeID); err != nil {
		return counts, notFound("fleet node", authenticatedNodeID, err)
	}

	instanceScopes := make(map[string]telemetryInstanceScope)
	lookupInstance := func(instanceID string) (telemetryInstanceScope, error) {
		if instanceID == "" {
			return telemetryInstanceScope{}, nil
		}
		if scope, ok := instanceScopes[instanceID]; ok {
			return scope, nil
		}
		if !validTelemetryResourceID(instanceID) {
			return telemetryInstanceScope{}, fmt.Errorf("%w: invalid telemetry instance identity", ErrInvalidArgument)
		}
		var scope telemetryInstanceScope
		err := tx.QueryRowContext(ctx, `
SELECT i.service_id, s.project_id
FROM porter_control_plane.instances i
JOIN porter_control_plane.services s ON s.id = i.service_id
WHERE i.id = $1 AND i.node_id = $2 AND i.state <> 'deleted'
FOR SHARE OF i`, instanceID, authenticatedNodeID).Scan(&scope.ServiceID, &scope.ProjectID)
		if err != nil {
			return telemetryInstanceScope{}, notFound("instance", instanceID, err)
		}
		instanceScopes[instanceID] = scope
		return scope, nil
	}

	receivedAt := now.UTC()
	expiresAt := receivedAt.Add(telemetryRetention)
	for _, record := range batch.Logs {
		scope, err := lookupInstance(record.InstanceID)
		if err != nil {
			return TelemetryIngestCounts{}, err
		}
		message := redactTelemetryMessage(record.Message)
		if message == "" {
			message = "[redacted]"
		}
		payload, err := json.Marshal(struct {
			InstanceID string    `json:"instance_id,omitempty"`
			ReportedAt time.Time `json:"reported_at"`
			Level      string    `json:"level"`
			Message    string    `json:"message"`
		}{record.InstanceID, record.ReportedAt.UTC(), record.Level, message})
		if err != nil {
			return TelemetryIngestCounts{}, fmt.Errorf("encode telemetry log hash input: %w", err)
		}
		hash := sha256.Sum256(payload)
		var insertedID int64
		err = tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.telemetry_logs
    (fleet_node_id, record_id, instance_id, project_id, service_id, reported_at, received_at, expires_at, level, message, payload_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (fleet_node_id, record_id) DO NOTHING
RETURNING id`, authenticatedNodeID, record.RecordID, nullableID(record.InstanceID), nullableID(scope.ProjectID), nullableID(scope.ServiceID), record.ReportedAt.UTC(), receivedAt, expiresAt, record.Level, message, hash[:]).Scan(&insertedID)
		if errors.Is(err, sql.ErrNoRows) {
			var previous []byte
			if err := tx.QueryRowContext(ctx, `SELECT payload_hash FROM porter_control_plane.telemetry_logs WHERE fleet_node_id=$1 AND record_id=$2`, authenticatedNodeID, record.RecordID).Scan(&previous); err != nil {
				return TelemetryIngestCounts{}, fmt.Errorf("read duplicate telemetry log: %w", err)
			}
			if !equalHash(previous, hash[:]) {
				return TelemetryIngestCounts{}, ErrTelemetryConflict
			}
			counts.Duplicates++
		} else if err != nil {
			return TelemetryIngestCounts{}, fmt.Errorf("insert telemetry log: %w", err)
		} else {
			counts.LogsAccepted++
		}
	}

	for _, record := range batch.Metrics {
		scope, err := lookupInstance(record.InstanceID)
		if err != nil {
			return TelemetryIngestCounts{}, err
		}
		if math.IsNaN(record.Value) || math.IsInf(record.Value, 0) {
			return TelemetryIngestCounts{}, fmt.Errorf("%w: invalid telemetry metric value", ErrInvalidArgument)
		}
		payload, err := json.Marshal(struct {
			InstanceID string    `json:"instance_id,omitempty"`
			ReportedAt time.Time `json:"reported_at"`
			Name       string    `json:"name"`
			Value      float64   `json:"value"`
			Unit       string    `json:"unit,omitempty"`
		}{record.InstanceID, record.ReportedAt.UTC(), record.Name, record.Value, record.Unit})
		if err != nil {
			return TelemetryIngestCounts{}, fmt.Errorf("encode telemetry metric hash input: %w", err)
		}
		hash := sha256.Sum256(payload)
		var insertedID int64
		err = tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.telemetry_metrics
    (fleet_node_id, record_id, instance_id, project_id, service_id, reported_at, received_at, expires_at, metric_name, metric_value, unit, payload_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (fleet_node_id, record_id) DO NOTHING
RETURNING id`, authenticatedNodeID, record.RecordID, nullableID(record.InstanceID), nullableID(scope.ProjectID), nullableID(scope.ServiceID), record.ReportedAt.UTC(), receivedAt, expiresAt, record.Name, record.Value, record.Unit, hash[:]).Scan(&insertedID)
		if errors.Is(err, sql.ErrNoRows) {
			var previous []byte
			if err := tx.QueryRowContext(ctx, `SELECT payload_hash FROM porter_control_plane.telemetry_metrics WHERE fleet_node_id=$1 AND record_id=$2`, authenticatedNodeID, record.RecordID).Scan(&previous); err != nil {
				return TelemetryIngestCounts{}, fmt.Errorf("read duplicate telemetry metric: %w", err)
			}
			if !equalHash(previous, hash[:]) {
				return TelemetryIngestCounts{}, ErrTelemetryConflict
			}
			counts.Duplicates++
		} else if err != nil {
			return TelemetryIngestCounts{}, fmt.Errorf("insert telemetry metric: %w", err)
		} else {
			counts.MetricsAccepted++
		}
	}

	if err := pruneNodeTelemetry(ctx, tx, authenticatedNodeID, receivedAt); err != nil {
		return TelemetryIngestCounts{}, err
	}
	if err := tx.Commit(); err != nil {
		return TelemetryIngestCounts{}, fmt.Errorf("commit telemetry ingestion: %w", err)
	}
	return counts, nil
}

type telemetryInstanceScope struct {
	ProjectID string
	ServiceID string
}

func validTelemetryResourceID(id string) bool {
	return len(id) > 0 && len(id) <= 128 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00\r\n\t ")
}

func redactTelemetryMessage(message string) string {
	message = telemetryBearerRE.ReplaceAllString(message, "$1[REDACTED]")
	message = telemetrySecretRE.ReplaceAllString(message, "$1[REDACTED]")
	message = telemetryPathRE.ReplaceAllString(message, "$1[agent-local path]")
	if len(message) > 4096 {
		message = message[:4096]
	}
	return strings.TrimSpace(message)
}

func equalHash(a, b []byte) bool {
	return len(a) == sha256.Size && len(b) == sha256.Size && strings.EqualFold(fmt.Sprintf("%x", a), fmt.Sprintf("%x", b))
}

func pruneNodeTelemetry(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time) error {
	if err := pruneTelemetryTable(ctx, tx, "telemetry_logs", nodeID, now, maxTelemetryLogsPerNode); err != nil {
		return err
	}
	if err := pruneTelemetryTable(ctx, tx, "telemetry_metrics", nodeID, now, maxTelemetryMetricsPerNode); err != nil {
		return err
	}
	return nil
}

func pruneTelemetryTable(ctx context.Context, tx *sql.Tx, table, nodeID string, now time.Time, maxRows int) error {
	// Table names are selected only by this internal allowlist, never by request input.
	if table != "telemetry_logs" && table != "telemetry_metrics" {
		return fmt.Errorf("invalid telemetry table")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM porter_control_plane.`+table+` WHERE fleet_node_id=$1 AND expires_at <= $2`, nodeID, now); err != nil {
		return fmt.Errorf("prune expired %s: %w", table, err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM porter_control_plane.`+table+` WHERE fleet_node_id=$1`, nodeID).Scan(&count); err != nil {
		return fmt.Errorf("count %s for retention: %w", table, err)
	}
	excess := count - maxRows
	if excess <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM porter_control_plane.`+table+` WHERE id IN (
	 SELECT id FROM porter_control_plane.`+table+` WHERE fleet_node_id=$1
	 ORDER BY reported_at ASC, id ASC LIMIT $2
	)`, nodeID, excess); err != nil {
		return fmt.Errorf("enforce %s per-node row cap: %w", table, err)
	}
	return nil
}

// pruneExpiredTelemetryTx performs a bounded global cleanup from the existing
// authenticated heartbeat path; no separate worker or external scheduler is needed.
func pruneExpiredTelemetryTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	for _, table := range []string{"telemetry_logs", "telemetry_metrics"} {
		if table != "telemetry_logs" && table != "telemetry_metrics" {
			return fmt.Errorf("invalid telemetry table")
		}
		if _, err := tx.ExecContext(ctx, `WITH expired AS (
	 SELECT id FROM porter_control_plane.`+table+` WHERE expires_at <= $1 ORDER BY expires_at, id LIMIT $2
	) DELETE FROM porter_control_plane.`+table+` WHERE id IN (SELECT id FROM expired)`, now, telemetryPruneBatch); err != nil {
			return fmt.Errorf("prune expired telemetry %s: %w", table, err)
		}
	}
	return nil
}

func validateTelemetryFilter(filter TelemetryFilter, now time.Time) error {
	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) || filter.To.Sub(filter.From) > telemetryRetention || filter.From.Before(now.UTC().Add(-telemetryRetention)) || filter.To.After(now.UTC().Add(time.Minute)) {
		return fmt.Errorf("%w: invalid telemetry time range", ErrInvalidArgument)
	}
	if (filter.NodeID != "" && !validTelemetryResourceID(filter.NodeID)) || (filter.InstanceID != "" && !validTelemetryResourceID(filter.InstanceID)) {
		return fmt.Errorf("%w: invalid telemetry resource filter", ErrInvalidArgument)
	}
	return nil
}

func parseTelemetryCursorID(cursor *EventCursor) (any, any, error) {
	if err := validateEventCursor(cursor); err != nil {
		return nil, nil, err
	}
	if cursor == nil {
		return nil, nil, nil
	}
	id, err := strconv.ParseInt(cursor.ID, 10, 64)
	if err != nil || id < 1 {
		return nil, nil, fmt.Errorf("%w: invalid telemetry cursor ID", ErrInvalidArgument)
	}
	return cursor.OccurredAt, id, nil
}

func (s *Store) ListTelemetryLogsPage(ctx context.Context, limit int, filter TelemetryFilter, after *EventCursor) ([]v1.TelemetryLog, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTelemetryFilter(filter, time.Now().UTC()); err != nil {
		return nil, err
	}
	cursorAt, cursorID, err := parseTelemetryCursorID(after)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT l.id, l.fleet_node_id, l.instance_id, l.project_id, l.service_id, l.reported_at, l.received_at, l.level, l.message
FROM porter_control_plane.telemetry_logs l
WHERE l.expires_at > now() AND l.reported_at >= $1 AND l.reported_at < $2
  AND ($3 = '' OR l.fleet_node_id = $3)
  AND ($4 = '' OR l.instance_id = $4)
  AND ($5::timestamptz IS NULL OR (l.reported_at, l.id) < ($5::timestamptz, $6::bigint))
ORDER BY l.reported_at DESC, l.id DESC
LIMIT $7`, filter.From.UTC(), filter.To.UTC(), filter.NodeID, filter.InstanceID, cursorAt, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list telemetry logs: %w", err)
	}
	return scanRows(rows, "telemetry log", scanTelemetryLog)
}

func scanTelemetryLog(row rowScanner) (v1.TelemetryLog, error) {
	var item v1.TelemetryLog
	var id int64
	var instanceID, projectID, serviceID sql.NullString
	if err := row.Scan(&id, &item.NodeID, &instanceID, &projectID, &serviceID, &item.ReportedAt, &item.ReceivedAt, &item.Level, &item.Message); err != nil {
		return item, err
	}
	item.ID = strconv.FormatInt(id, 10)
	if instanceID.Valid { item.InstanceID = instanceID.String }
	if projectID.Valid { item.ProjectID = projectID.String }
	if serviceID.Valid { item.ServiceID = serviceID.String }
	return item, nil
}

func (s *Store) ListTelemetryMetricsPage(ctx context.Context, limit int, filter TelemetryFilter, after *EventCursor) ([]v1.TelemetryMetric, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTelemetryFilter(filter, time.Now().UTC()); err != nil {
		return nil, err
	}
	cursorAt, cursorID, err := parseTelemetryCursorID(after)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.fleet_node_id, m.instance_id, m.project_id, m.service_id, m.reported_at, m.received_at, m.metric_name, m.metric_value, m.unit
FROM porter_control_plane.telemetry_metrics m
WHERE m.expires_at > now() AND m.reported_at >= $1 AND m.reported_at < $2
  AND ($3 = '' OR m.fleet_node_id = $3)
  AND ($4 = '' OR m.instance_id = $4)
  AND ($5::timestamptz IS NULL OR (m.reported_at, m.id) < ($5::timestamptz, $6::bigint))
ORDER BY m.reported_at DESC, m.id DESC
LIMIT $7`, filter.From.UTC(), filter.To.UTC(), filter.NodeID, filter.InstanceID, cursorAt, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list telemetry metrics: %w", err)
	}
	return scanRows(rows, "telemetry metric", scanTelemetryMetric)
}

func scanTelemetryMetric(row rowScanner) (v1.TelemetryMetric, error) {
	var item v1.TelemetryMetric
	var id int64
	var instanceID, projectID, serviceID sql.NullString
	if err := row.Scan(&id, &item.NodeID, &instanceID, &projectID, &serviceID, &item.ReportedAt, &item.ReceivedAt, &item.Name, &item.Value, &item.Unit); err != nil {
		return item, err
	}
	item.ID = strconv.FormatInt(id, 10)
	if instanceID.Valid { item.InstanceID = instanceID.String }
	if projectID.Valid { item.ProjectID = projectID.String }
	if serviceID.Valid { item.ServiceID = serviceID.String }
	return item, nil
}
