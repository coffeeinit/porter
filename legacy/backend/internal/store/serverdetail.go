// Per-server detail surface (WP7): everything the server detail page needs in
// one round trip — the servers row, the node_heartbeats agent report, live
// capacity vs allocated, VM counts by state, and the daemon-log tail for that
// node. VM→node scoping rides on the placements table (workload_id =
// replicas.id, state <> 'released'); there is no per-server host metrics
// table, so the timeseries aggregates per-VM metrics_samples instead.
package store

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"time"

	"porter/internal/types"
)

// ServerNodeHeartbeat is the node_heartbeats row for one server (latest agent
// liveness + free-capacity report). Capabilities is the raw JSONB blob.
type ServerNodeHeartbeat struct {
	AgentVersion string         `json:"agent_version"`
	VCPUFree     int            `json:"vcpu_free"`
	MemFreeMiB   int            `json:"mem_free_mib"`
	VMCount      int            `json:"vm_count"`
	Capabilities map[string]any `json:"capabilities"`
	ReportedAt   time.Time      `json:"reported_at"`
}

// ServerCapacity is what the node reports it has vs what its placed VMs
// reserve. Free values are nil until the first agent heartbeat arrives.
type ServerCapacity struct {
	VCPUsTotal      int   `json:"vcpus_total"`       // servers.vcpus (heartbeat-reported host total)
	MemTotalMiB     int   `json:"mem_total_mib"`     // servers.mem_mib
	VCPUsAllocated  int64 `json:"vcpus_allocated"`   // sum of placed VM reservations (replicas.data)
	MemAllocatedMiB int64 `json:"mem_allocated_mib"` // sum of placed VM reservations (replicas.data)
	VCPUsFree       *int  `json:"vcpus_free"`        // node_heartbeats.vcpu_free (null = no heartbeat yet)
	MemFreeMiB      *int  `json:"mem_free_mib"`      // node_heartbeats.mem_free_mib
}

// ServerVMRow is one replica placed on a server, joined with its project name.
type ServerVMRow struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ProjectID      string     `json:"project_id"`
	ProjectName    string     `json:"project_name"`
	ServiceName    string     `json:"service_name"`
	State          string     `json:"state"`
	HealthStatus   string     `json:"health_status"`
	ReplicaIndex   int        `json:"replica_index"`
	IPAddress      string     `json:"ip_address"`
	VCPUs          int        `json:"vcpus"`
	MemMiB         int        `json:"mem_mib"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	PlacementState string     `json:"placement_state"`
}

// ServerLogLine is one daemon-log row scoped to a server (best-effort match on
// the node's hostname/id; daemon_logs is a global stream, not per-server).
type ServerLogLine struct {
	ID   int64     `json:"id"`
	TS   time.Time `json:"ts"`
	Line string    `json:"line"`
}

// ServerDetailPoint is one timeseries bucket: fixed-width seconds since epoch,
// aggregated over every sample that falls in the bucket across the node's VMs.
type ServerDetailPoint struct {
	T   int64   `json:"t"` // bucket start, unix seconds
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
	Sum float64 `json:"sum"` // meaningful for memory_mib (total across VMs)
	N   int     `json:"n"`   // samples in bucket
}

// ServerDetail is the per-server detail bundle behind GET /servers/{id}/detail.
type ServerDetail struct {
	Server     *types.Server        `json:"server"`
	Heartbeat  *ServerNodeHeartbeat `json:"heartbeat"` // null until the agent reports
	Capacity   ServerCapacity       `json:"capacity"`
	VMCounts   map[string]int       `json:"vm_counts"` // state -> count, for placed VMs
	VMsPlaced  int                  `json:"vms_placed"`
	RecentLogs []ServerLogLine      `json:"recent_logs"`
}

// GetServerDetail assembles the full per-server detail bundle. Returns false
// when the server is not registered.
func (s *Store) GetServerDetail(serverID string) (*ServerDetail, bool) {
	srv, ok := s.GetServer(serverID)
	if !ok {
		return nil, false
	}
	d := &ServerDetail{
		Server:     srv,
		VMCounts:   map[string]int{},
		RecentLogs: []ServerLogLine{},
	}
	if hb, ok := s.ServerHeartbeat(serverID); ok {
		d.Heartbeat = hb
		vf, mf := hb.VCPUFree, hb.MemFreeMiB
		d.Capacity.VCPUsFree = &vf
		d.Capacity.MemFreeMiB = &mf
	}
	d.Capacity.VCPUsTotal = srv.VCPUs
	d.Capacity.MemTotalMiB = srv.MemMiB

	// Placed VMs: count by state and sum their resource reservations
	// (vcpus/mem_mib live in the replicas data JSONB, so guard the cast).
	rows, err := s.pool.Query(context.Background(), `
		SELECT r.state, COUNT(*),
			SUM(CASE WHEN r.data->>'vcpus'   ~ '^[0-9]+$' THEN (r.data->>'vcpus')::int   ELSE 0 END),
			SUM(CASE WHEN r.data->>'mem_mib' ~ '^[0-9]+$' THEN (r.data->>'mem_mib')::int ELSE 0 END)
		FROM placements pl
		JOIN replicas r ON r.id::text = pl.workload_id
		WHERE pl.node_id = $1 AND pl.state <> 'released'
		GROUP BY r.state`, serverID)
	if err != nil {
		log.Printf("store: server detail vms %s: %v", serverID, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var state string
			var n int64
			var vcpu, mem int64
			if err := rows.Scan(&state, &n, &vcpu, &mem); err != nil {
				continue
			}
			d.VMCounts[state] = int(n)
			d.VMsPlaced += int(n)
			d.Capacity.VCPUsAllocated += vcpu
			d.Capacity.MemAllocatedMiB += mem
		}
	}
	d.RecentLogs = s.ServerDaemonLogs(serverID, 20)
	return d, true
}

// PurgeServerData removes the per-node rows that outlive the servers row:
// placements, the node heartbeat, the placed VMs' metric samples, and the
// daemon-log lines that mention the node. Call it alongside DeleteServer —
// deleting the servers row alone orphans placement and heartbeat data.
func (s *Store) PurgeServerData(serverID string) {
	hostname := ""
	if srv, ok := s.GetServer(serverID); ok && srv != nil {
		hostname = srv.Name
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM metrics_samples WHERE vm_id::text IN
			(SELECT workload_id FROM placements WHERE node_id = $1)`, []any{serverID}},
		{`DELETE FROM placements WHERE node_id = $1`, []any{serverID}},
		{`DELETE FROM node_heartbeats WHERE node_id = $1`, []any{serverID}},
	} {
		if _, err := s.pool.Exec(context.Background(), q.sql, q.args...); err != nil {
			log.Printf("store: purge server data %s: %v", serverID, err)
		}
	}
	if hostname != "" {
		_, _ = s.pool.Exec(context.Background(),
			`DELETE FROM daemon_logs WHERE line LIKE '%' || $1 || '%' OR line LIKE '%' || $2 || '%'`,
			hostname, serverID)
	}
}

// ServerHeartbeat returns the latest node_heartbeats row for a server
// (node_id = server id). Returns false when no agent heartbeat exists yet.
func (s *Store) ServerHeartbeat(serverID string) (*ServerNodeHeartbeat, bool) {
	var hb ServerNodeHeartbeat
	var caps []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT agent_version, vcpu_free, mem_free_mib, vm_count, capabilities, reported_at
		 FROM node_heartbeats WHERE node_id = $1`, serverID).
		Scan(&hb.AgentVersion, &hb.VCPUFree, &hb.MemFreeMiB, &hb.VMCount, &caps, &hb.ReportedAt)
	if err != nil {
		return nil, false
	}
	hb.Capabilities = map[string]any{}
	if len(caps) > 0 {
		_ = json.Unmarshal(caps, &hb.Capabilities)
	}
	return &hb, true
}

// ServerVMsList lists the replicas placed on a server (placements active or
// reserved), newest first, with project names. limit<=0 means 200; capped 1000.
func (s *Store) ServerVMsList(serverID string, limit int) []ServerVMRow {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT r.id::text, COALESCE(r.data->>'name',''), COALESCE(r.project_id::text,''),
			COALESCE(p.name,''), COALESCE(r.data->>'service_name',''),
			r.state, r.health_status, r.replica_index, COALESCE(r.ip_address::text,''),
			CASE WHEN r.data->>'vcpus'   ~ '^[0-9]+$' THEN (r.data->>'vcpus')::int   ELSE 0 END,
			CASE WHEN r.data->>'mem_mib' ~ '^[0-9]+$' THEN (r.data->>'mem_mib')::int ELSE 0 END,
			r.started_at, r.created_at, pl.state
		FROM placements pl
		JOIN replicas r ON r.id::text = pl.workload_id
		LEFT JOIN projects p ON p.id = r.project_id
		WHERE pl.node_id = $1 AND pl.state <> 'released'
		ORDER BY r.created_at DESC
		LIMIT $2`, serverID, limit)
	if err != nil {
		log.Printf("store: server vms list %s: %v", serverID, err)
		return nil
	}
	defer rows.Close()
	out := make([]ServerVMRow, 0)
	for rows.Next() {
		var v ServerVMRow
		if err := rows.Scan(&v.ID, &v.Name, &v.ProjectID, &v.ProjectName, &v.ServiceName,
			&v.State, &v.HealthStatus, &v.ReplicaIndex, &v.IPAddress,
			&v.VCPUs, &v.MemMiB, &v.StartedAt, &v.CreatedAt, &v.PlacementState); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// ServerMetricsTimeseries aggregates the server's per-VM metrics_samples into
// fixed-step buckets. There is no per-server host metrics table — the series
// are averages/sums across the VMs placed on the node (placements), which is
// the closest real source. Metric names come straight from the DB (the
// collector writes cpu_percent and memory_mib).
func (s *Store) ServerMetricsTimeseries(serverID string, from, to time.Time, stepSeconds int) map[string][]ServerDetailPoint {
	if stepSeconds <= 0 {
		stepSeconds = 60
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if !from.Before(to) {
		to = from.Add(time.Hour)
	}
	out := map[string][]ServerDetailPoint{}
	rows, err := s.pool.Query(context.Background(), `
		SELECT (FLOOR(EXTRACT(EPOCH FROM ms.ts) / $3) * $3)::bigint AS bucket,
		       ms.metric, COUNT(*), AVG(ms.value), MAX(ms.value), SUM(ms.value)
		FROM metrics_samples ms
		WHERE ms.ts >= $1 AND ms.ts < $2
		  AND ms.vm_id::text IN (
		      SELECT pl.workload_id FROM placements pl
		      WHERE pl.node_id = $4 AND pl.state <> 'released')
		GROUP BY 1, ms.metric
		ORDER BY 1`, from, to, stepSeconds, serverID)
	if err != nil {
		log.Printf("store: server metrics timeseries %s: %v", serverID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var bucket int64
		var metric string
		var n int64
		var avg, max, sum float64
		if err := rows.Scan(&bucket, &metric, &n, &avg, &max, &sum); err != nil {
			continue
		}
		out[metric] = append(out[metric], ServerDetailPoint{T: bucket, Avg: avg, Max: max, Sum: sum, N: int(n)})
	}
	return out
}

// ServerDaemonLogs returns the newest durable daemon-log lines for a server.
// daemon_logs is a global stream, so scoping is a best-effort match on the
// node's hostname or id (registration/unregistration lines mention them).
// limit<=0 means 100; capped 1000.
func (s *Store) ServerDaemonLogs(serverID string, limit int) []ServerLogLine {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	pats := []string{}
	if srv, ok := s.GetServer(serverID); ok && srv.Name != "" {
		pats = append(pats, "%"+escapeLike(srv.Name)+"%")
	}
	pats = append(pats, "%"+escapeLike(serverID)+"%")

	args := []any{limit}
	where := ""
	for _, p := range pats {
		args = append(args, p)
		if where != "" {
			where += " OR "
		}
		where += "line ILIKE $" + strconv.Itoa(len(args)) + " ESCAPE '\\'"
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, ts, line FROM daemon_logs WHERE `+where+` ORDER BY id DESC LIMIT $1`, args...)
	if err != nil {
		log.Printf("store: server daemon logs %s: %v", serverID, err)
		return nil
	}
	defer rows.Close()
	out := make([]ServerLogLine, 0)
	for rows.Next() {
		var l ServerLogLine
		if err := rows.Scan(&l.ID, &l.TS, &l.Line); err != nil {
			continue
		}
		out = append(out, l)
	}
	return out
}
