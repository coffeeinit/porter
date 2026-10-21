package store

import (
	"context"
	"log"
	"time"
)

// This file persists scheduler placements (reserved/active/released +
// home-node affinity), capacity policies (host>pool>provider>global) and
// one-time enrollment tokens plus node heartbeats for the host reconciler.

// Placement is the stored form of a scheduler placement.
type Placement struct {
	ID          string
	WorkloadID  string
	NodeID      string
	HomeNodeID  string
	VmIP        string
	State       string
	OperationID string
}

// ReservePlacement inserts a reserved placement and returns its id.
func (s *Store) ReservePlacement(workloadID, nodeID, vmIP, operationID string) (string, error) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO placements (workload_id, node_id, home_node_id, vm_ip, state, operation_id, reserved_at)
		 VALUES ($1, $2, $2, $3, 'reserved', $4, now()) RETURNING id::text`,
		workloadID, nodeID, vmIP, operationID).Scan(&id)
	if err != nil {
		log.Printf("store: reserve placement: %v", err)
	}
	return id, err
}

// SetPlacementState moves a placement through its lifecycle.
func (s *Store) SetPlacementState(id, state string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE placements SET state = $2,
		 activated_at = CASE WHEN $2 = 'active' THEN now() ELSE activated_at END,
		 released_at = CASE WHEN $2 = 'released' THEN now() ELSE released_at END
		 WHERE id::text = $1`, id, state)
	if err != nil {
		log.Printf("store: set placement state: %v", err)
	}
	return err
}

// ActivePlacement returns the newest non-released placement for a workload.
func (s *Store) ActivePlacement(workloadID string) (Placement, bool) {
	var p Placement
	err := s.pool.QueryRow(context.Background(),
		`SELECT id::text, workload_id, node_id, home_node_id, vm_ip, state, operation_id
		 FROM placements WHERE workload_id = $1 AND state <> 'released'
		 ORDER BY created_at DESC LIMIT 1`, workloadID).
		Scan(&p.ID, &p.WorkloadID, &p.NodeID, &p.HomeNodeID, &p.VmIP, &p.State, &p.OperationID)
	if err != nil {
		return Placement{}, false
	}
	return p, true
}

// CapacityPolicyRow is the stored effective-capacity policy for one scope.
type CapacityPolicyRow struct {
	Scope         string
	ScopeID       string
	CPUOvercommit *float64
	MemOvercommit *float64
	ReserveVCPU   *int
	ReserveMemMiB *int
	Enabled       *bool
}

// UpsertCapacityPolicy writes one scope row (host>pool>provider>global).
func (s *Store) UpsertCapacityPolicy(p CapacityPolicyRow) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO capacity_policies (scope, scope_id, cpu_overcommit, mem_overcommit,
		 reserve_vcpu, reserve_mem_mib, enabled, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		 ON CONFLICT (scope, scope_id) DO UPDATE SET
		 cpu_overcommit = EXCLUDED.cpu_overcommit, mem_overcommit = EXCLUDED.mem_overcommit,
		 reserve_vcpu = EXCLUDED.reserve_vcpu, reserve_mem_mib = EXCLUDED.reserve_mem_mib,
		 enabled = EXCLUDED.enabled, updated_at = now()`,
		p.Scope, p.ScopeID, p.CPUOvercommit, p.MemOvercommit, p.ReserveVCPU, p.ReserveMemMiB, p.Enabled)
	if err != nil {
		log.Printf("store: upsert capacity policy: %v", err)
	}
	return err
}

// ListCapacityPolicies returns all scope rows for policy.Merge.
func (s *Store) ListCapacityPolicies() []CapacityPolicyRow {
	rows, err := s.pool.Query(context.Background(),
		`SELECT scope, scope_id, cpu_overcommit, mem_overcommit,
		reserve_vcpu, reserve_mem_mib, enabled FROM capacity_policies`)
	if err != nil {
		log.Printf("store: list capacity policies: %v", err)
		return nil
	}
	defer rows.Close()
	out := []CapacityPolicyRow{}
	for rows.Next() {
		var p CapacityPolicyRow
		if err := rows.Scan(&p.Scope, &p.ScopeID, &p.CPUOvercommit, &p.MemOvercommit,
			&p.ReserveVCPU, &p.ReserveMemMiB, &p.Enabled); err != nil {
			log.Printf("store: scan capacity policy: %v", err)
			return out
		}
		out = append(out, p)
	}
	return out
}

// Heartbeat is one agent liveness report.
type Heartbeat struct {
	NodeID       string
	AgentVersion string
	VCPUFree     int
	MemFreeMiB   int
	VMCount      int
	ReportedAt   time.Time
}

// PutHeartbeat upserts the latest agent report per node.
func (s *Store) PutHeartbeat(h Heartbeat) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO node_heartbeats (node_id, agent_version, vcpu_free, mem_free_mib,
		 vm_count, reported_at) VALUES ($1,$2,$3,$4,$5,now())
		 ON CONFLICT (node_id) DO UPDATE SET agent_version = EXCLUDED.agent_version,
		 vcpu_free = EXCLUDED.vcpu_free, mem_free_mib = EXCLUDED.mem_free_mib,
		 vm_count = EXCLUDED.vm_count, reported_at = now()`,
		h.NodeID, h.AgentVersion, h.VCPUFree, h.MemFreeMiB, h.VMCount)
	if err != nil {
		log.Printf("store: put heartbeat: %v", err)
	}
	return err
}

// StaleHeartbeats returns nodes silent since the cutoff (host reconciler).
func (s *Store) StaleHeartbeats(olderThan time.Time) []Heartbeat {
	rows, err := s.pool.Query(context.Background(),
		`SELECT node_id, agent_version, vcpu_free, mem_free_mib, vm_count, reported_at
		 FROM node_heartbeats WHERE reported_at < $1 ORDER BY reported_at ASC LIMIT 200`, olderThan)
	if err != nil {
		log.Printf("store: stale heartbeats: %v", err)
		return nil
	}
	defer rows.Close()
	out := []Heartbeat{}
	for rows.Next() {
		var h Heartbeat
		if err := rows.Scan(&h.NodeID, &h.AgentVersion, &h.VCPUFree, &h.MemFreeMiB,
			&h.VMCount, &h.ReportedAt); err != nil {
			log.Printf("store: scan heartbeat: %v", err)
			return out
		}
		out = append(out, h)
	}
	return out
}

// CreateEnrollmentToken stores a one-time host bootstrap token.
func (s *Store) CreateEnrollmentToken(token, provider, labels string, expiresAt time.Time) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO enrollment_tokens (token, provider, labels, expires_at)
		 VALUES ($1, $2, $3::jsonb, $4)`, token, provider, labels, expiresAt)
	if err != nil {
		log.Printf("store: create enrollment token: %v", err)
	}
	return err
}

// ConsumeEnrollmentToken marks a valid unused token consumed by a node.
// Returns false when the token is missing, used, or expired.
func (s *Store) ConsumeEnrollmentToken(token, nodeID string) bool {
	var ok bool
	err := s.pool.QueryRow(context.Background(),
		`UPDATE enrollment_tokens SET used_by = $2 WHERE token = $1
		 AND used_by = '' AND expires_at > now() RETURNING TRUE`, token, nodeID).Scan(&ok)
	return err == nil && ok
}
