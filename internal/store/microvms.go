package store

import (
	"context"
	"log"
)

// This file persists MicroVM runtime truth (task T8, migration 0017).
// ReplicaController writes these rows on boot/delete/state transitions so the
// service→deployment→replica→micro_vm chain is queryable durably.

// UpsertMicroVM inserts or updates a MicroVM row.
func (s *Store) UpsertMicroVM(id, replicaID, nodeID, state string, cpuMillicores, memMiB int) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO micro_vms (id, replica_id, node_id, state, cpu_millicores, mem_mib, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,now())
		 ON CONFLICT (id) DO UPDATE SET replica_id=EXCLUDED.replica_id, node_id=EXCLUDED.node_id,
		   state=EXCLUDED.state, cpu_millicores=EXCLUDED.cpu_millicores, mem_mib=EXCLUDED.mem_mib,
		   updated_at=now()`,
		id, replicaID, nodeID, state, cpuMillicores, memMiB)
	if err != nil {
		log.Printf("store: upsert micro_vm: %v", err)
	}
	return err
}

// SetMicroVMState updates only the lifecycle state.
func (s *Store) SetMicroVMState(id, state string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE micro_vms SET state=$2, updated_at=now() WHERE id=$1`, id, state)
	if err != nil {
		log.Printf("store: set micro_vm state: %v", err)
	}
	return err
}

// DeleteMicroVM removes a MicroVM row.
func (s *Store) DeleteMicroVM(id string) error {
	_, err := s.pool.Exec(context.Background(), `DELETE FROM micro_vms WHERE id=$1`, id)
	if err != nil {
		log.Printf("store: delete micro_vm: %v", err)
	}
	return err
}

// ListMicroVMsByReplica returns MicroVM ids for a replica.
func (s *Store) ListMicroVMsByReplica(replicaID string) []string {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id FROM micro_vms WHERE replica_id=$1 ORDER BY created_at`, replicaID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}
