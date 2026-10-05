package store

// VPS mode (WP1): a project flipped to workload_type='vps' is persistent —
// its replica pool never scales to zero and its IP allocations stay pinned.
// Project state lives in the projects.data JSON blob (PutProject serializes
// the whole types.Project there); the workload_type/persistent columns added
// by migration 0036 are the SQL query surface, kept in sync with the blob in
// the same write so callers never observe a half-flipped project.

import (
	"context"
	"fmt"
	"log"

	"porter/internal/types"
)

// SetProjectWorkloadType flips a project between the microvm and vps workload
// types. The 0036 columns and the projects.data JSON blob move together, and
// the project cache is invalidated, so GetProject and GetProjectWorkloadType
// agree immediately. Idempotent by construction. Returns an error for any
// workload type other than microvm|vps, and a not-found error when the
// project row does not exist.
func (s *Store) SetProjectWorkloadType(projectID, workloadType string, persistent bool) error {
	if workloadType != types.WorkloadTypeMicroVM && workloadType != types.WorkloadTypeVPS {
		return fmt.Errorf("invalid workload type %q (want %q or %q)",
			workloadType, types.WorkloadTypeMicroVM, types.WorkloadTypeVPS)
	}
	ctx := context.Background()
	tag, err := s.pool.Exec(ctx, `
		UPDATE projects SET
			workload_type = $2, persistent = $3, updated_at = now(),
			data = jsonb_set(jsonb_set(data, '{workload_type}', to_jsonb($2::text)),
			                 '{persistent}', to_jsonb($3::boolean))
		WHERE id = $1`, projectID, workloadType, persistent)
	if err != nil {
		log.Printf("store: set workload type for %s: %v", projectID, err)
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project %s not found", projectID)
	}
	s.cacheDel(ctx, "project:"+projectID)
	return nil
}

// GetProjectWorkloadType returns the current workload type and persistence
// flag. found is false when the project does not exist; rows written before
// migration 0036 read back as ("microvm", false) via the column defaults.
func (s *Store) GetProjectWorkloadType(projectID string) (workloadType string, persistent bool, found bool) {
	err := s.pool.QueryRow(context.Background(),
		`SELECT workload_type, persistent FROM projects WHERE id = $1`, projectID).
		Scan(&workloadType, &persistent)
	if err != nil {
		return types.WorkloadTypeMicroVM, false, false
	}
	if workloadType == "" {
		workloadType = types.WorkloadTypeMicroVM
	}
	return workloadType, persistent, true
}

// VPSProject is one persistent project in an org-wide VPS listing.
type VPSProject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	VMCount int    `json:"vm_count"`
}

// ListVPSProjects returns the persistent (vps) projects of an org with their
// replica counts.
func (s *Store) ListVPSProjects(orgID string) []VPSProject {
	rows, err := s.pool.Query(context.Background(), `
		SELECT p.id::text, p.name,
		       (SELECT count(*) FROM replicas r WHERE r.project_id = p.id) AS vm_count
		FROM projects p
		WHERE p.persistent = TRUE AND p.workload_type = 'vps' AND p.org_id::text = $1
		ORDER BY p.created_at`, orgID)
	if err != nil {
		log.Printf("store: list vps projects for org %s: %v", orgID, err)
		return nil
	}
	defer rows.Close()
	out := make([]VPSProject, 0)
	for rows.Next() {
		var v VPSProject
		if err := rows.Scan(&v.ID, &v.Name, &v.VMCount); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// PinVMStaticIP marks a VM's durable IP allocation(s) pinned (migration 0036
// adds ip_allocations.pinned). An empty ip pins every allocation row of the
// VM; a specific ip pins just that row. The pinned flag is the store-level
// contract: the allocator must not recycle pinned addresses and ephemeral
// teardown must not release them. Attaching a brand-new static address on a
// real host is KVM-metal work — planned, not implemented here (SRS §66).
func (s *Store) PinVMStaticIP(vmID, ip string) error {
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE ip_allocations SET pinned = TRUE
		 WHERE micro_vm_id = $1 AND ($2 = '' OR host(ip) = $2)`, vmID, ip)
	if err != nil {
		log.Printf("store: pin static ip for vm %s: %v", vmID, err)
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("vm %s has no ip allocation to pin (static-ip host attach is planned)", vmID)
	}
	return nil
}

// UnpinVMStaticIP is the inverse of PinVMStaticIP: it clears the pinned flag
// when a project converts back to the ephemeral microvm mode. Missing
// allocation rows are not an error.
func (s *Store) UnpinVMStaticIP(vmID string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE ip_allocations SET pinned = FALSE WHERE micro_vm_id = $1`, vmID)
	if err != nil {
		log.Printf("store: unpin static ip for vm %s: %v", vmID, err)
	}
	return err
}

// PinnedIP is one pinned allocation reported by ListPinnedVMIPs.
type PinnedIP struct {
	VMID string `json:"vm_id"`
	IP   string `json:"ip"`
}

// ListPinnedVMIPs returns the pinned allocations of a project's VMs.
func (s *Store) ListPinnedVMIPs(projectID string) []PinnedIP {
	rows, err := s.pool.Query(context.Background(), `
		SELECT a.micro_vm_id, host(a.ip)
		FROM ip_allocations a
		JOIN replicas r ON r.id::text = a.micro_vm_id
		WHERE r.project_id = $1 AND a.pinned
		ORDER BY a.ip`, projectID)
	if err != nil {
		log.Printf("store: list pinned ips for project %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]PinnedIP, 0)
	for rows.Next() {
		var p PinnedIP
		if err := rows.Scan(&p.VMID, &p.IP); err == nil {
			out = append(out, p)
		}
	}
	return out
}
