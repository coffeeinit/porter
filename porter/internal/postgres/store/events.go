package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	v1 "porter/controlplane/contract/v1"
)

// EventCursor is the descending keyset boundary for (occurred_at, id).
type EventCursor struct {
	OccurredAt time.Time
	ID         string
}

type eventRefs struct {
	projectID      string
	serviceID      string
	releaseID      string
	instanceID     string
	fleetNodeID    string
	domainClaimID  string
	gatewayRouteID string
	volumeID       string
	snapshotID     string
	backupID       string
	operationID    string
	commandID      string
}

func (s *Store) appendEvent(ctx context.Context, tx *sql.Tx, eventType string, refs eventRefs, data any) error {
	id, err := newID("evt")
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode resource event data: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO porter_control_plane.resource_events
	    (id, event_type, project_id, service_id, release_id, instance_id, fleet_node_id,
	     domain_claim_id, gateway_route_id, volume_id, snapshot_id, backup_id, operation_id, command_id, data)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`,
		id, eventType, nullableID(refs.projectID), nullableID(refs.serviceID), nullableID(refs.releaseID),
		nullableID(refs.instanceID), nullableID(refs.fleetNodeID), nullableID(refs.domainClaimID),
		nullableID(refs.gatewayRouteID), nullableID(refs.volumeID), nullableID(refs.snapshotID),
		nullableID(refs.backupID), nullableID(refs.operationID), nullableID(refs.commandID), string(encoded)); err != nil {
		return fmt.Errorf("append resource event %q: %w", eventType, err)
	}
	return nil
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (s *Store) ListEventsPage(ctx context.Context, limit int, after *EventCursor) ([]v1.Event, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateEventCursor(after); err != nil {
		return nil, err
	}
	var occurredAt, id any
	if after != nil {
		occurredAt, id = after.OccurredAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, event_type, occurred_at, project_id, service_id, release_id, instance_id,
	       fleet_node_id, domain_claim_id, gateway_route_id, volume_id, snapshot_id, backup_id,
	       operation_id, command_id, data
FROM porter_control_plane.resource_events
WHERE $1::timestamptz IS NULL OR (occurred_at, id) < ($1::timestamptz, $2::text)
ORDER BY occurred_at DESC, id DESC
LIMIT $3`, occurredAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list resource event page: %w", err)
	}
	return scanRows(rows, "resource event", scanEvent)
}

func validateEventCursor(cursor *EventCursor) error {
	if cursor != nil && (cursor.OccurredAt.IsZero() || cursor.ID == "") {
		return fmt.Errorf("%w: invalid event cursor", ErrInvalidArgument)
	}
	return nil
}

func scanEvent(row rowScanner) (v1.Event, error) {
	var event v1.Event
	var projectID, serviceID, releaseID, instanceID, fleetNodeID, domainClaimID, gatewayRouteID sql.NullString
	var volumeID, snapshotID, backupID, operationID, commandID sql.NullString
	var data []byte
	if err := row.Scan(&event.ID, &event.Type, &event.OccurredAt, &projectID, &serviceID, &releaseID,
		&instanceID, &fleetNodeID, &domainClaimID, &gatewayRouteID, &volumeID, &snapshotID, &backupID,
		&operationID, &commandID, &data); err != nil {
		return v1.Event{}, err
	}
	if projectID.Valid {
		event.ProjectID = projectID.String
	}
	if serviceID.Valid {
		event.ServiceID = serviceID.String
	}
	if releaseID.Valid {
		event.ReleaseID = releaseID.String
	}
	if instanceID.Valid {
		event.InstanceID = instanceID.String
	}
	if fleetNodeID.Valid {
		event.FleetNodeID = fleetNodeID.String
	}
	if domainClaimID.Valid {
		event.DomainClaimID = domainClaimID.String
	}
	if gatewayRouteID.Valid {
		event.GatewayRouteID = gatewayRouteID.String
	}
	if volumeID.Valid {
		event.VolumeID = volumeID.String
	}
	if snapshotID.Valid {
		event.SnapshotID = snapshotID.String
	}
	if backupID.Valid {
		event.BackupID = backupID.String
	}
	if operationID.Valid {
		event.OperationID = operationID.String
	}
	if commandID.Valid {
		event.CommandID = commandID.String
	}
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	event.Data = append(json.RawMessage(nil), data...)
	return event, nil
}
