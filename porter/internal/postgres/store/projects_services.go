package store

import (
	"context"
	"fmt"
	"strings"

	v1 "porter/controlplane/contract/v1"
)

func (s *Store) CreateProject(ctx context.Context, name string) (v1.Project, error) {
	name = strings.TrimSpace(name)
	if err := validateName("project", name); err != nil {
		return v1.Project{}, err
	}
	id, err := newID("prj")
	if err != nil {
		return v1.Project{}, err
	}
	var project v1.Project
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Project{}, fmt.Errorf("begin project creation: %w", err)
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.projects (id, name)
VALUES ($1, $2)
RETURNING id, name, state, created_at, updated_at`, id, name).
		Scan(&project.ID, &project.Name, &project.State, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return v1.Project{}, fmt.Errorf("create project: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "project.created", eventRefs{projectID: project.ID}, map[string]any{"name": project.Name}); err != nil {
		return v1.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Project{}, fmt.Errorf("commit project creation: %w", err)
	}
	return project, nil
}

func (s *Store) GetProject(ctx context.Context, id string) (v1.Project, error) {
	var project v1.Project
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, state, created_at, updated_at
FROM porter_control_plane.projects WHERE id = $1`, id).
		Scan(&project.ID, &project.Name, &project.State, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return v1.Project{}, notFound("project", id, err)
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]v1.Project, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, state, created_at, updated_at
FROM porter_control_plane.projects
ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := make([]v1.Project, 0)
	for rows.Next() {
		var project v1.Project
		if err := rows.Scan(&project.ID, &project.Name, &project.State, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

func (s *Store) SetProjectState(ctx context.Context, id string, state v1.ProjectState) error {
	if state != v1.ProjectActive && state != v1.ProjectDeleting {
		return fmt.Errorf("%w: unsupported project state %q", ErrInvalidArgument, state)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project state update: %w", err)
	}
	defer tx.Rollback()
	var previous v1.ProjectState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.projects WHERE id = $1 FOR UPDATE`, id).Scan(&previous); err != nil {
		return notFound("project", id, err)
	}
	if previous == state {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE porter_control_plane.projects SET state = $1, updated_at = now() WHERE id = $2`, state, id); err != nil {
		return fmt.Errorf("set project state: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "project.state_changed", eventRefs{projectID: id}, map[string]any{"from": previous, "to": state}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project state update: %w", err)
	}
	return nil
}

func (s *Store) CreateService(ctx context.Context, projectID string, request v1.CreateServiceRequest) (v1.Service, error) {
	request.Name = strings.TrimSpace(request.Name)
	if err := validateName("service", request.Name); err != nil {
		return v1.Service{}, err
	}
	if request.Port == 0 {
		request.Port = 8080
	}
	if request.Port < 1 || request.Port > 65535 {
		return v1.Service{}, fmt.Errorf("%w: service port must be between 1 and 65535", ErrInvalidArgument)
	}
	if request.VCPUs < 0 || request.MemoryMB < 0 || request.DesiredInstances < 0 {
		return v1.Service{}, fmt.Errorf("%w: service resources and desired instances cannot be negative", ErrInvalidArgument)
	}
	if request.VCPUs == 0 {
		request.VCPUs = 1
	}
	if request.MemoryMB == 0 {
		request.MemoryMB = 512
	}
	if request.DesiredInstances == 0 {
		request.DesiredInstances = 1
	}
	if request.Runtime == "" {
		request.Runtime = v1.FirecrackerRuntime
	}
	if request.Runtime != v1.FirecrackerRuntime {
		return v1.Service{}, fmt.Errorf("%w: production service runtime must be %q", ErrInvalidArgument, v1.FirecrackerRuntime)
	}
	id, err := newID("svc")
	if err != nil {
		return v1.Service{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v1.Service{}, fmt.Errorf("begin service creation: %w", err)
	}
	defer tx.Rollback()
	var projectState v1.ProjectState
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM porter_control_plane.projects WHERE id = $1 FOR UPDATE`, projectID).Scan(&projectState); err != nil {
		return v1.Service{}, notFound("project", projectID, err)
	}
	if projectState != v1.ProjectActive {
		return v1.Service{}, fmt.Errorf("%w: project %q is %q", ErrFailedPrecondition, projectID, projectState)
	}

	var service v1.Service
	err = tx.QueryRowContext(ctx, `
INSERT INTO porter_control_plane.services
    (id, project_id, name, runtime, port, vcpus, memory_mb, desired_instances)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, project_id, name, state, runtime, port, vcpus, memory_mb,
          desired_instances, created_at, updated_at`,
		id, projectID, request.Name, request.Runtime, request.Port, request.VCPUs,
		request.MemoryMB, request.DesiredInstances).
		Scan(&service.ID, &service.ProjectID, &service.Name, &service.State, &service.Runtime,
			&service.Port, &service.VCPUs, &service.MemoryMB, &service.DesiredInstances,
			&service.CreatedAt, &service.UpdatedAt)
	if err != nil {
		return v1.Service{}, fmt.Errorf("create service: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "service.created", eventRefs{projectID: service.ProjectID, serviceID: service.ID}, map[string]any{
		"name": service.Name, "runtime": service.Runtime, "port": service.Port,
		"vcpus": service.VCPUs, "memory_mb": service.MemoryMB, "desired_instances": service.DesiredInstances,
	}); err != nil {
		return v1.Service{}, err
	}
	if err := tx.Commit(); err != nil {
		return v1.Service{}, fmt.Errorf("commit service creation: %w", err)
	}
	return service, nil
}

func (s *Store) GetService(ctx context.Context, id string) (v1.Service, error) {
	var service v1.Service
	err := s.db.QueryRowContext(ctx, `
SELECT id, project_id, name, state, runtime, port, vcpus, memory_mb,
       desired_instances, created_at, updated_at
FROM porter_control_plane.services WHERE id = $1`, id).
		Scan(&service.ID, &service.ProjectID, &service.Name, &service.State, &service.Runtime,
			&service.Port, &service.VCPUs, &service.MemoryMB, &service.DesiredInstances,
			&service.CreatedAt, &service.UpdatedAt)
	if err != nil {
		return v1.Service{}, notFound("service", id, err)
	}
	return service, nil
}

func (s *Store) ListServices(ctx context.Context, projectID string) ([]v1.Service, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, name, state, runtime, port, vcpus, memory_mb,
       desired_instances, created_at, updated_at
FROM porter_control_plane.services WHERE project_id = $1
ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	services := make([]v1.Service, 0)
	for rows.Next() {
		var service v1.Service
		if err := rows.Scan(&service.ID, &service.ProjectID, &service.Name, &service.State, &service.Runtime,
			&service.Port, &service.VCPUs, &service.MemoryMB, &service.DesiredInstances,
			&service.CreatedAt, &service.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}
	return services, nil
}

func (s *Store) SetServiceState(ctx context.Context, id string, state v1.ServiceState) error {
	if state != v1.ServiceActive && state != v1.ServiceDeleting {
		return fmt.Errorf("%w: unsupported service state %q", ErrInvalidArgument, state)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin service state update: %w", err)
	}
	defer tx.Rollback()
	var previous v1.ServiceState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, id).Scan(&previous); err != nil {
		return notFound("service", id, err)
	}
	if previous == state {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE porter_control_plane.services SET state = $1, updated_at = now() WHERE id = $2`, state, id); err != nil {
		return fmt.Errorf("set service state: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "service.state_changed", eventRefs{serviceID: id}, map[string]any{"from": previous, "to": state}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit service state update: %w", err)
	}
	return nil
}

// SetDesiredInstances persists desired capacity only. It does not schedule or
// start, stop, or otherwise mutate Firecracker instances.
func (s *Store) SetDesiredInstances(ctx context.Context, id string, desired int) error {
	if desired < 0 {
		return fmt.Errorf("%w: desired instances cannot be negative", ErrInvalidArgument)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin desired instance update: %w", err)
	}
	defer tx.Rollback()
	var previous int
	if err := tx.QueryRowContext(ctx, `SELECT desired_instances FROM porter_control_plane.services WHERE id = $1 FOR UPDATE`, id).Scan(&previous); err != nil {
		return notFound("service", id, err)
	}
	if previous == desired {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE porter_control_plane.services SET desired_instances = $1, updated_at = now() WHERE id = $2`, desired, id); err != nil {
		return fmt.Errorf("set desired instances: %w", err)
	}
	if err := s.appendEvent(ctx, tx, "service.desired_instances_changed", eventRefs{serviceID: id}, map[string]any{"from": previous, "to": desired}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit desired instance update: %w", err)
	}
	return nil
}
