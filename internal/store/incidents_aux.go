// Incident + aux-VM persistence (migration 0031): alerts escalate into
// incidents here, and auxiliary VM pairings (builder/browser/bastion per
// workload) are recorded here when provisioned.
package store

import (
	"context"
	"log"
	"time"
)

// Incident is one tracked operational event.
type Incident struct {
	ID        string
	ProjectID string
	Title     string
	Severity  string
	State     string
	Summary   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateIncident opens an incident (severity free-form; lifecycle owns states).
func (s *Store) CreateIncident(projectID, title, severity, summary string) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO incidents (project_id, title, severity, summary)
		 VALUES ($1,$2,$3,$4) RETURNING id::text`,
		projectID, title, severity, summary).Scan(&id)
	if err != nil {
		log.Printf("store: create incident: %v", err)
	}
	return id, err
}

// ListIncidents returns newest-first incidents for a project.
func (s *Store) ListIncidents(projectID string) []Incident {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id::text, project_id, title, severity, state, summary,
		 created_at, updated_at FROM incidents
		 WHERE project_id = $1 ORDER BY created_at DESC LIMIT 100`, projectID)
	if err != nil {
		log.Printf("store: list incidents: %v", err)
		return nil
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var in Incident
		if err := rows.Scan(&in.ID, &in.ProjectID, &in.Title, &in.Severity,
			&in.State, &in.Summary, &in.CreatedAt, &in.UpdatedAt); err != nil {
			continue
		}
		out = append(out, in)
	}
	return out
}

// AuxVMRow is one recorded auxiliary VM pairing.
type AuxVMRow struct {
	ID         string
	WorkloadID string
	Kind       string
	NodeID     string
	CreatedAt  time.Time
}

// RecordAuxVM stores one pairing (idempotent by id).
func (s *Store) RecordAuxVM(id, workloadID, kind, nodeID string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO aux_vms (id, workload_id, kind, node_id)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`,
		id, workloadID, kind, nodeID)
	if err != nil {
		log.Printf("store: record aux vm: %v", err)
	}
	return err
}

// ListAuxVMs returns pairings for a workload.
func (s *Store) ListAuxVMs(workloadID string) []AuxVMRow {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, workload_id, kind, node_id, created_at FROM aux_vms
		 WHERE workload_id = $1 ORDER BY created_at ASC`, workloadID)
	if err != nil {
		log.Printf("store: list aux vms: %v", err)
		return nil
	}
	defer rows.Close()
	var out []AuxVMRow
	for rows.Next() {
		var r AuxVMRow
		if err := rows.Scan(&r.ID, &r.WorkloadID, &r.Kind, &r.NodeID, &r.CreatedAt); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// DeleteAuxVM removes one pairing record (the VM itself is stopped/destroyed
// by the caller through the runtime first).
func (s *Store) DeleteAuxVM(id string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM aux_vms WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete aux vm: %v", err)
		return false
	}
	return res.RowsAffected() > 0
}
