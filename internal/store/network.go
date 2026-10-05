package store

import (
	"context"
	"log"

	"porter/internal/resource"
)

// This file persists IP allocations backing the per-VM allocator (task T6):
// the allocator computes deterministically, the table records durably so
// restarts and concurrent controllers never double-allocate.

// AllocateIP records an IP allocation for a MicroVM on a network.
// Re-allocating the same (network, ip) is a no-op returning the existing row.
func (s *Store) AllocateIP(networkID, ip, microVMID string) (*resource.IPAllocation, error) {
	var id int64
	err := s.pool.QueryRow(context.Background(),
		`INSERT INTO ip_allocations (network_id, ip, micro_vm_id)
		 VALUES ($1, $2::inet, $3)
		 ON CONFLICT (network_id, ip) DO UPDATE SET micro_vm_id = EXCLUDED.micro_vm_id
		 RETURNING id`,
		networkID, ip, microVMID).Scan(&id)
	if err != nil {
		log.Printf("store: allocate ip: %v", err)
		return nil, err
	}
	return &resource.IPAllocation{
		Spec: resource.IPAllocationSpec{NetworkID: networkID, IP: ip, MicroVMID: microVMID},
	}, nil
}

// ReleaseIP frees the allocation for a MicroVM.
func (s *Store) ReleaseIP(microVMID string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM ip_allocations WHERE micro_vm_id = $1`, microVMID)
	if err != nil {
		log.Printf("store: release ip: %v", err)
	}
	return err
}

// ListIPAllocations returns allocations on a network.
func (s *Store) ListIPAllocations(networkID string) []resource.IPAllocation {
	rows, err := s.pool.Query(context.Background(),
		`SELECT network_id, host(ip), micro_vm_id FROM ip_allocations WHERE network_id = $1 ORDER BY ip`,
		networkID)
	if err != nil {
		log.Printf("store: list ip allocations: %v", err)
		return nil
	}
	defer rows.Close()
	var out []resource.IPAllocation
	for rows.Next() {
		var a resource.IPAllocation
		var vmID *string
		if err := rows.Scan(&a.Spec.NetworkID, &a.Spec.IP, &vmID); err != nil {
			continue
		}
		if vmID != nil {
			a.Spec.MicroVMID = *vmID
		}
		out = append(out, a)
	}
	return out
}
