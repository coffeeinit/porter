package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	v1 "porter/controlplane/contract/v1"
)

// TimeCursor is the keyset boundary for collections ordered by (created_at, id).
type TimeCursor struct {
	CreatedAt time.Time
	ID        string
}

// ReleaseCursor is the keyset boundary for release history ordered by
// (sequence DESC, id ASC).
type ReleaseCursor struct {
	Sequence int64
	ID       string
}

func validatePageLimit(limit int) error {
	// The extra item used by the HTTP adapter signals whether a next cursor is
	// needed, so one more than the public maximum is accepted internally.
	if limit < 1 || limit > v1.MaxPageSize+1 {
		return fmt.Errorf("%w: page limit must be between 1 and %d", ErrInvalidArgument, v1.MaxPageSize+1)
	}
	return nil
}

func validateTimeCursor(cursor *TimeCursor) error {
	if cursor != nil && (cursor.CreatedAt.IsZero() || cursor.ID == "") {
		return fmt.Errorf("%w: invalid time cursor", ErrInvalidArgument)
	}
	return nil
}

func validateReleaseCursor(cursor *ReleaseCursor) error {
	if cursor != nil && (cursor.Sequence < 1 || cursor.ID == "") {
		return fmt.Errorf("%w: invalid release cursor", ErrInvalidArgument)
	}
	return nil
}

func (s *Store) ListProjectsPage(ctx context.Context, limit int, after *TimeCursor) ([]v1.Project, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, state, created_at, updated_at
FROM porter_control_plane.projects
WHERE $1::timestamptz IS NULL OR (created_at, id) > ($1::timestamptz, $2::text)
ORDER BY created_at, id
LIMIT $3`, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list project page: %w", err)
	}
	return scanRows(rows, "project", scanProject)
}

func (s *Store) ListServicesPage(ctx context.Context, projectID string, limit int, after *TimeCursor) ([]v1.Service, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, name, state, runtime, port, vcpus, memory_mb,
       desired_instances, created_at, updated_at
FROM porter_control_plane.services
WHERE project_id = $1
  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2::timestamptz, $3::text))
ORDER BY created_at, id
LIMIT $4`, projectID, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list service page: %w", err)
	}
	return scanRows(rows, "service", scanService)
}

func (s *Store) ListReleasesPage(ctx context.Context, serviceID string, limit int, after *ReleaseCursor) ([]v1.Release, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateReleaseCursor(after); err != nil {
		return nil, err
	}
	var sequence, id any
	if after != nil {
		sequence, id = after.Sequence, after.ID
	}
	rows, err := s.db.QueryContext(ctx, releaseSelect+`
WHERE service_id = $1
  AND ($2::bigint IS NULL OR sequence < $2 OR (sequence = $2 AND id > $3))
ORDER BY sequence DESC, id
LIMIT $4`, serviceID, sequence, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list release page: %w", err)
	}
	return scanRows(rows, "release", scanRelease)
}

func (s *Store) ListInstancesPage(ctx context.Context, releaseID string, limit int, after *TimeCursor) ([]v1.Instance, error) {
	if err := validatePageLimit(limit); err != nil {
		return nil, err
	}
	if err := validateTimeCursor(after); err != nil {
		return nil, err
	}
	var createdAt, id any
	if after != nil {
		createdAt, id = after.CreatedAt, after.ID
	}
	rows, err := s.db.QueryContext(ctx, instanceSelect+`
WHERE release_id = $1
  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2::timestamptz, $3::text))
ORDER BY created_at, id
LIMIT $4`, releaseID, createdAt, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list instance page: %w", err)
	}
	return scanRows(rows, "instance", scanInstance)
}

func scanProject(row rowScanner) (v1.Project, error) {
	var project v1.Project
	err := row.Scan(&project.ID, &project.Name, &project.State, &project.CreatedAt, &project.UpdatedAt)
	return project, err
}

func scanService(row rowScanner) (v1.Service, error) {
	var service v1.Service
	err := row.Scan(&service.ID, &service.ProjectID, &service.Name, &service.State, &service.Runtime,
		&service.Port, &service.VCPUs, &service.MemoryMB, &service.DesiredInstances,
		&service.CreatedAt, &service.UpdatedAt)
	return service, err
}

func scanRows[T any](rows *sql.Rows, kind string, scan func(rowScanner) (T, error)) ([]T, error) {
	defer rows.Close()
	items := make([]T, 0)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s page: %w", kind, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s page: %w", kind, err)
	}
	return items, nil
}
