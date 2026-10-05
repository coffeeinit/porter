// Package store persists Porter state in PostgreSQL. Only this package is
// allowed to import the pgx driver (Unified Spec §6).
//
// Model (v3): a project IS a microVM app with a replica pool. Replicas live
// inside their project. Every user auto-gets a default org; groups are folders
// inside an org. No managed volumes (persistence = project host_mount_path);
// no image catalog (image is a plain OCI ref).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"porter/internal/auth"
	"porter/internal/cache"
	"porter/internal/observability"
	"porter/internal/types"
	"porter/migrations"
)

// Store is the single real state store: a pgx pool plus three in-memory ring
// buffers (traffic + per-VM logs + web-vitals beacons) that are explicitly
// not persisted.
type Store struct {
	pool *pgxpool.Pool

	cache cache.Cache // optional read-through cache; nil = Noop (off)

	trafficMu sync.RWMutex
	traffic   map[string][]*types.TrafficEntry // replica_id -> ring (in-memory)

	vitalsMu sync.RWMutex
	vitals   map[string][]*types.WebVital // project_id -> ring (in-memory)

	logMu      sync.RWMutex
	logs       map[string][]string
	daemonLogs []string
}

const (
	trafficRingSize   = 200
	logRingSize       = 500
	daemonLogRingSize = 1000
)

// NewStore connects to PostgreSQL at dsn, runs pending migrations, and returns
// a ready store. Fatal on failure — the server must not start without its DB.
func NewStore(dsn string) *Store {
	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("store: parse postgres config %q: %v", dsn, err)
	}
	poolConfig.ConnConfig.Tracer = observability.NewPGXTracer()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatalf("store: connect postgres %q: %v", dsn, err)
	}
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("store: ping postgres %q: %v", dsn, err)
	}
	if err := Migrate(ctx, pool); err != nil {
		log.Fatalf("store: migrate: %v", err)
	}
	return &Store{
		pool:    pool,
		traffic: map[string][]*types.TrafficEntry{},
		vitals:  map[string][]*types.WebVital{},
		logs:    map[string][]string{},
	}
}

// Close releases the pool.
func (s *Store) Close() error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

// NewID returns a random UUID string.
func NewID() string {
	return uuid.NewString()
}

// SetCache attaches an optional read-through cache (nil disables → Noop).
func (s *Store) SetCache(c cache.Cache) {
	if c == nil {
		c = cache.Noop{}
	}
	s.cache = c
}

// cachedGet reads a key through the cache, falling back to load() on a miss.
// The result is cached as the JSON of what load returns for ttl.
func (s *Store) cachedGet(ctx context.Context, key string, ttl time.Duration, load func() ([]byte, error)) ([]byte, error) {
	if s.cache != nil {
		if b, ok := s.cache.Get(ctx, key); ok {
			return b, nil
		}
	}
	b, err := load()
	if err != nil {
		return nil, err
	}
	if s.cache != nil && ttl > 0 {
		_ = s.cache.Set(ctx, key, b, ttl)
	}
	return b, nil
}

// cacheDel removes one or more keys from the cache (no-op when off).
func (s *Store) cacheDel(ctx context.Context, keys ...string) {
	if s.cache != nil {
		_ = s.cache.Del(ctx, keys...)
	}
}

// Migrate runs every NNNN_*.up.sql in order, tracking applied versions.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)
	for _, name := range ups {
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		for _, stmt := range splitSQL(string(body)) {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("apply migration %s: %w\nstmt: %s", name, err, stmt)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		log.Printf("store: applied migration %s", name)
	}
	return nil
}

func splitSQL(body string) []string {
	var out []string
	var cur strings.Builder
	inDollar := false // inside $$-quoted (DO block / function body)
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		// Toggle dollar-quoting: a line containing $$ starts/ends a quoted body.
		// We keep whole DO blocks together (they contain internal ';').
		if strings.Contains(t, "$$") {
			inDollar = !inDollar
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if !inDollar && strings.HasSuffix(strings.TrimSpace(line), ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("store: marshal json: %v", err)
		return []byte("{}")
	}
	return b
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- Replicas (one microVM; nested inside a project) ---
// Backed by the `replicas` table (renamed from `vms` in 0010). The VM methods
// are kept so the vmmanager executor works unchanged.

func (s *Store) PutVM(vm *types.VM) {
	ctx := context.Background()
	data := mustJSON(vm)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO replicas (id, project_id, replica_index, container_id, task_id,
		                      state, health_status, ip_address, ports, started_at,
		                      crashed, data, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			project_id=EXCLUDED.project_id, replica_index=EXCLUDED.replica_index,
			container_id=EXCLUDED.container_id, task_id=EXCLUDED.task_id,
			state=EXCLUDED.state, health_status=EXCLUDED.health_status,
			ip_address=EXCLUDED.ip_address, ports=EXCLUDED.ports,
			started_at=EXCLUDED.started_at, crashed=EXCLUDED.crashed,
			data=EXCLUDED.data, updated_at=now()`,
		vm.ID, nullableStr(vm.ProjectID), vm.ReplicaIndex, nullableStr(vm.ContainerID),
		nullableStr(vm.TaskID), vm.State, vm.HealthStatus, nullableStr(vm.IPAddress),
		string(mustJSON(vm.Ports)), vm.StartedAt, vm.Crashed, string(data))
	if err != nil {
		log.Printf("store: put replica %s: %v", vm.ID, err)
	}
}

func (s *Store) GetVM(id string) (*types.VM, bool) {
	var data []byte
	if err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM replicas WHERE id = $1`, id).Scan(&data); err != nil {
		return nil, false
	}
	var vm types.VM
	if err := json.Unmarshal(data, &vm); err != nil {
		log.Printf("store: unmarshal replica %s: %v", id, err)
		return nil, false
	}
	return &vm, true
}

func (s *Store) ListVMs() []*types.VM {
	rows, err := s.pool.Query(context.Background(), `SELECT data FROM replicas ORDER BY created_at`)
	if err != nil {
		log.Printf("store: list replicas: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.VM, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var vm types.VM
		if err := json.Unmarshal(data, &vm); err != nil {
			continue
		}
		out = append(out, &vm)
	}
	return out
}

// ListReplicas returns all replicas of a project.
func (s *Store) ListReplicas(projectID string) []*types.VM {
	rows, err := s.pool.Query(context.Background(),
		`SELECT data FROM replicas WHERE project_id = $1 ORDER BY replica_index`, projectID)
	if err != nil {
		log.Printf("store: list replicas for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.VM, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var vm types.VM
		if err := json.Unmarshal(data, &vm); err != nil {
			continue
		}
		out = append(out, &vm)
	}
	return out
}

func (s *Store) DeleteVM(id string) {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM replicas WHERE id = $1`, id); err != nil {
		log.Printf("store: delete replica %s: %v", id, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM domains WHERE vm_id::text = $1 OR data->>'vm_id' = $1`, id); err != nil {
		log.Printf("store: delete domains for %s: %v", id, err)
	}
	s.trafficMu.Lock()
	delete(s.traffic, id)
	s.trafficMu.Unlock()
	s.logMu.Lock()
	delete(s.logs, id)
	s.logMu.Unlock()
}

// DeleteReplicasByProject removes every replica row for a project. Needed on
// redeploy/delete so a fresh pool doesn't collide with the
// UNIQUE(project_id, replica_index) constraint.
func (s *Store) DeleteReplicasByProject(projectID string) {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM replicas WHERE project_id = $1`, projectID); err != nil {
		log.Printf("store: delete replicas for project %s: %v", projectID, err)
	}
}

// --- Projects (the microVM app; one row, owns the replica pool) ---

func (s *Store) PutProject(p *types.Project) error {
	ctx := context.Background()
	data := mustJSON(p)
	s.cacheDel(ctx, "project:"+p.ID)
	kind := "single_image"
	if p.Source == "compose" {
		kind = "compose"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projects (id, name, kind, compose_yaml, image, org_id,
		                      host_mount_path, replicas_desired, restart_policy,
		                      healthcheck, env, status, data, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, kind=EXCLUDED.kind, compose_yaml=EXCLUDED.compose_yaml,
			image=EXCLUDED.image, org_id=EXCLUDED.org_id, host_mount_path=EXCLUDED.host_mount_path,
			replicas_desired=EXCLUDED.replicas_desired, restart_policy=EXCLUDED.restart_policy,
			healthcheck=EXCLUDED.healthcheck, env=EXCLUDED.env, status=EXCLUDED.status,
			data=EXCLUDED.data, created_by=EXCLUDED.created_by, updated_at=now()`,
		p.ID, p.Name, kind, nullableStr(p.ComposeYAML), nullableStr(p.Image),
		nullableStr(p.OrgID), nullableStr(p.HostMountPath), p.ReplicasDesired,
		p.RestartPolicy, mustJSON(p.Healthcheck), string(mustJSON(p.Env)),
		statusFromProject(p), string(data), p.CreatedBy)
	if err != nil {
		log.Printf("store: put project %s: %v", p.ID, err)
	}
	return err
}

func (s *Store) GetProject(id string) (*types.Project, bool) {
	data, err := s.cachedGet(context.Background(), "project:"+id, 15*time.Second, func() ([]byte, error) {
		var raw []byte
		if qerr := s.pool.QueryRow(context.Background(),
			`SELECT data FROM projects WHERE id = $1`, id).Scan(&raw); qerr != nil {
			return nil, qerr
		}
		return raw, nil
	})
	if err != nil {
		return nil, false
	}
	var p types.Project
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("store: unmarshal project %s: %v", id, err)
		return nil, false
	}
	return &p, true
}

func (s *Store) ListProjects() []*types.Project {
	rows, err := s.pool.Query(context.Background(), `SELECT data FROM projects ORDER BY created_at`)
	if err != nil {
		log.Printf("store: list projects: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Project, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var p types.Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out
}

func (s *Store) ListProjectsByOrg(orgID string) []*types.Project {
	rows, err := s.pool.Query(context.Background(),
		`SELECT data FROM projects WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		log.Printf("store: list projects for org %s: %v", orgID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Project, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var p types.Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out
}

func (s *Store) DeleteProject(id string) {
	if _, err := s.pool.Exec(context.Background(),
		`DELETE FROM projects WHERE id = $1`, id); err != nil {
		log.Printf("store: delete project %s: %v", id, err)
	}
}

func (s *Store) SetProjectOrg(projectID, orgID string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE projects SET org_id = $2 WHERE id = $1`, projectID, nullableStr(orgID))
	return err
}

func (s *Store) SetProjectTags(projectID string, tags []string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE projects SET tags = $2 WHERE id = $1`, projectID, tags)
	return err
}

// --- Orgs (auto-created default org per user) ---

// EnsureDefaultOrg creates a default org for a user if none exists. Called on
// first admin login / signup so GET /orgs/default always resolves.
func (s *Store) EnsureDefaultOrg(ctx context.Context, ownerID, name string) (string, error) {
	var existing string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM orgs WHERE owner_id = $1 AND is_default = true`, ownerID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	id := NewID()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO orgs (id, name, owner_id, is_default) VALUES ($1,$2,$3,true)`,
		id, name, ownerID)
	if err != nil {
		return "", err
	}
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO org_members (org_id, user_id, role) VALUES ($1,$2,'owner') ON CONFLICT DO NOTHING`,
		id, ownerID)
	return id, nil
}

func (s *Store) ListOrgMembers(orgID string) []*types.OrgMember {
	rows, err := s.pool.Query(context.Background(), `
		SELECT om.org_id, om.user_id, u.username, om.role
		FROM org_members om JOIN users u ON u.id = om.user_id
		WHERE om.org_id = $1 ORDER BY u.username`, orgID)
	if err != nil {
		log.Printf("store: list org members for %s: %v", orgID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.OrgMember, 0)
	for rows.Next() {
		m := &types.OrgMember{}
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Username, &m.Role); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) AddOrgMember(orgID, userID, role string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO org_members (org_id, user_id, role) VALUES ($1,$2,$3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role`, orgID, userID, role)
	return err
}

func (s *Store) SetOrgMemberRole(orgID, userID, role string) bool {
	res, err := s.pool.Exec(context.Background(),
		`UPDATE org_members SET role = $3 WHERE org_id = $1 AND user_id = $2`, orgID, userID, role)
	return err == nil && res.RowsAffected() > 0
}

func (s *Store) DeleteOrgMember(orgID, userID string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	return err == nil && res.RowsAffected() > 0
}

func (s *Store) PutOrg(o *types.Org) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO orgs (id, name, owner_id, is_default) VALUES ($1,$2,$3,false)`,
		o.ID, o.Name, nullableStr(o.OwnerID))
	return err
}

func (s *Store) ListOrgs() []*types.Org {
	rows, err := s.pool.Query(context.Background(), `SELECT id, name FROM orgs ORDER BY name`)
	if err != nil {
		log.Printf("store: list orgs: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Org, 0)
	for rows.Next() {
		var o types.Org
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			continue
		}
		out = append(out, &o)
	}
	return out
}

func (s *Store) GetOrg(id string) (*types.Org, bool) {
	var o types.Org
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id, name, owner_id, is_default FROM orgs WHERE id = $1`, id).
		Scan(&o.ID, &o.Name, &o.OwnerID, &o.IsDefault); err != nil {
		return nil, false
	}
	return &o, true
}

// --- Groups (folders for related projects, scoped to an org) ---

func (s *Store) PutGroup(g *types.Group) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO groups (id, org_id, name) VALUES ($1,$2,$3)`,
		g.ID, nullableStr(g.OrgID), g.Name)
	return err
}

func (s *Store) ListGroups(orgID string) []*types.Group {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, org_id, name FROM groups WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		log.Printf("store: list groups for %s: %v", orgID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Group, 0)
	for rows.Next() {
		var g types.Group
		if err := rows.Scan(&g.ID, &g.OrgID, &g.Name); err != nil {
			continue
		}
		out = append(out, &g)
	}
	return out
}

func (s *Store) AddProjectToGroup(groupID, projectID string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO group_projects (group_id, project_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		groupID, projectID)
	return err
}

func (s *Store) GetGroup(id string) (*types.Group, bool) {
	var g types.Group
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(org_id::text,''), name FROM groups WHERE id = $1`, id).
		Scan(&g.ID, &g.OrgID, &g.Name); err != nil {
		return nil, false
	}
	return &g, true
}

func (s *Store) DeleteGroup(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM groups WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete group %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

func (s *Store) RemoveProjectFromGroup(groupID, projectID string) error {
	_, err := s.pool.Exec(context.Background(), `
		DELETE FROM group_projects WHERE group_id = $1 AND project_id = $2`,
		groupID, projectID)
	return err
}

func (s *Store) ListProjectsInGroup(groupID string) []*types.Project {
	rows, err := s.pool.Query(context.Background(), `
		SELECT p.data FROM group_projects gp JOIN projects p ON p.id = gp.project_id
		WHERE gp.group_id = $1 ORDER BY p.created_at`, groupID)
	if err != nil {
		log.Printf("store: list projects in group %s: %v", groupID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Project, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var p types.Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out
}

// --- Domains (attached to a project; routes to its replica pool) ---

func (s *Store) AddDomain(projectID string, d *types.Domain) {
	data := mustJSON(d)
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO domains (project_id, domain, kind, status, data, created_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (domain) DO UPDATE SET
			project_id=EXCLUDED.project_id, kind=EXCLUDED.kind, status=EXCLUDED.status, data=EXCLUDED.data`,
		nullableStr(projectID), d.Domain, d.Type, d.Status, string(data))
	if err != nil {
		log.Printf("store: add domain %s for project %s: %v", d.Domain, projectID, err)
	}
}

func (s *Store) ListDomains(projectID string) []*types.Domain {
	rows, err := s.pool.Query(context.Background(), `
		SELECT data FROM domains WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		log.Printf("store: list domains for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Domain, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var d types.Domain
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}
		out = append(out, &d)
	}
	return out
}

// ListProjectDomains aliases ListDomains under the gateway-facing name:
// domain rows attach to projects, never to individual VMs.
func (s *Store) ListProjectDomains(projectID string) []*types.Domain {
	return s.ListDomains(projectID)
}

// DeleteDomain removes one domain record (preview cleanup on deploy delete).
func (s *Store) DeleteDomain(projectID, domain string) {
	if domain == "" {
		return
	}
	if _, err := s.pool.Exec(context.Background(),
		`DELETE FROM domains WHERE project_id = $1 AND domain = $2`, projectID, domain); err != nil {
		log.Printf("store: delete domain %s: %v", domain, err)
	}
}

func (s *Store) RemoveDomain(projectID, domain string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM domains WHERE domain = $2 AND project_id = $1`, projectID, domain)
	if err != nil {
		log.Printf("store: remove domain %s for %s: %v", domain, projectID, err)
		return false
	}
	return res.RowsAffected() > 0
}

// --- DNS records (per project) ---

func (s *Store) AddDNSRecord(r *types.DNSRecord) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO dns_records (id, project_id, name, type, value, ttl, created_at)
		VALUES ($1,$2,$3,$4,$5,$6, now())
		ON CONFLICT (project_id, name) DO UPDATE SET type=EXCLUDED.type, value=EXCLUDED.value, ttl=EXCLUDED.ttl`,
		r.ID, nullableStr(r.ProjectID), r.Name, r.Type, r.Value, r.TTL)
	return err
}

func (s *Store) ListDNSRecords(projectID string) []*types.DNSRecord {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, project_id, name, type, value, ttl FROM dns_records
		WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		log.Printf("store: list dns for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.DNSRecord, 0)
	for rows.Next() {
		var r types.DNSRecord
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Type, &r.Value, &r.TTL); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out
}

func (s *Store) RemoveDNSRecord(projectID, name string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM dns_records WHERE project_id = $1 AND name = $2`, projectID, name)
	return err
}

// --- Build logs (rolling, per project) ---

func (s *Store) AppendBuildLog(projectID, line string) {
	s.AppendBuildLogFor(projectID, "", line)
	return
}

// AppendBuildLogFor persists a project build line with an optional build ID.
// The nullable build ID preserves compatibility with historical project-level
// lines while allowing live build streams to remain scoped to one build.
func (s *Store) AppendBuildLogFor(projectID, buildID, line string) {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO build_logs (project_id, build_id, line) VALUES ($1,$2,$3)`, projectID, nullStr(buildID), line)
	if err != nil {
		log.Printf("store: append build log for %s: %v", projectID, err)
	}
}

func (s *Store) TailBuildLogs(projectID string, n int) []string {
	if n <= 0 {
		n = 200
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT line FROM (SELECT id, line FROM build_logs WHERE project_id = $1 ORDER BY id DESC LIMIT $2) t
		ORDER BY id ASC`, projectID, n)
	if err != nil {
		log.Printf("store: tail build logs for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			continue
		}
		out = append(out, line)
	}
	return out
}

// --- Deployments (version history / rollback) ---

// TailBuildLogsFor returns only the lines for one build, oldest first.
func (s *Store) TailBuildLogsFor(projectID, buildID string, n int) []string {
	if n <= 0 {
		n = 200
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT line FROM (SELECT id, line FROM build_logs
		 WHERE project_id = $1 AND build_id = $2 ORDER BY id DESC LIMIT $3) t
		ORDER BY id ASC`, projectID, buildID, n)
	if err != nil {
		log.Printf("store: tail build logs for %s/%s: %v", projectID, buildID, err)
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			continue
		}
		out = append(out, line)
	}
	return out
}

func (s *Store) CreateDeployment(d *types.Deployment) error {
	if d.RouteWeight == 0 && d.RolloutPercent != 0 {
		d.RouteWeight = d.RolloutPercent
	}
	if d.RolloutPercent == 0 {
		d.RolloutPercent = d.RouteWeight
	}
	checks, _ := json.Marshal(d.Checks)
	if checks == nil {
		checks = []byte("[]")
	}
	vmIDs, _ := json.Marshal(d.VMIDs)
	if vmIDs == nil {
		vmIDs = []byte("[]")
	}
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO deployments (id, project_id, build_id, build_status, image_digest, rollback_to, git_url, git_commit, checks, rollout_percent, version_label, guest_base, environment, is_production, route_weight, vm_ids, preview_url, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17, now())
		ON CONFLICT (id) DO UPDATE SET
				build_status    = EXCLUDED.build_status,
				build_id        = EXCLUDED.build_id,
			image_digest    = EXCLUDED.image_digest,
			rollback_to     = EXCLUDED.rollback_to,
			git_url         = EXCLUDED.git_url,
			git_commit      = EXCLUDED.git_commit,
			checks          = EXCLUDED.checks,
			rollout_percent = EXCLUDED.rollout_percent,
			version_label   = EXCLUDED.version_label,
			environment     = EXCLUDED.environment,
			is_production   = EXCLUDED.is_production,
			route_weight    = EXCLUDED.route_weight,
			vm_ids          = EXCLUDED.vm_ids,
			preview_url     = EXCLUDED.preview_url`,
		d.ID, nullableStr(d.ProjectID), nullableStr(d.BuildID), d.BuildStatus, nullableStr(d.ImageDigest),
		nullableStr(d.RollbackTo), nullableStr(d.GitURL), nullableStr(d.GitCommit),
		string(checks), d.RolloutPercent, nullableStr(d.VersionLabel), nullableStr(d.GuestBase),
		nullableStr(d.Environment), d.IsProduction, d.RouteWeight, string(vmIDs), d.PreviewURL)

	return err
}

func (s *Store) ListDeployments(projectID string) []*types.Deployment {
	if projectID == "" {
		return nil
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, project_id, COALESCE(build_id::text,''), COALESCE(build_status,''), COALESCE(image_digest,''), COALESCE(rollback_to::text,''), COALESCE(git_url,''), COALESCE(git_commit,''),
		       COALESCE(checks,'[]'::jsonb), COALESCE(rollout_percent,100),
		       COALESCE(version_label,''), COALESCE(guest_base,'alpine'), COALESCE(environment,'preview'), COALESCE(is_production,false),
		       COALESCE(route_weight,0), COALESCE(vm_ids,'[]'::jsonb), COALESCE(preview_url,''), created_at FROM deployments
		WHERE project_id = $1 ORDER BY revision DESC`, projectID)
	if err != nil {
		log.Printf("store: list deployments for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Deployment, 0)
	for rows.Next() {
		var d types.Deployment
		var checks []byte
		var vmIDs []byte
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.BuildID, &d.BuildStatus, &d.ImageDigest,
			&d.RollbackTo, &d.GitURL, &d.GitCommit, &checks, &d.RolloutPercent,
			&d.VersionLabel, &d.GuestBase, &d.Environment, &d.IsProduction, &d.RouteWeight, &vmIDs, &d.PreviewURL, &d.CreatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal(checks, &d.Checks)
		_ = json.Unmarshal(vmIDs, &d.VMIDs)
		if d.RouteWeight == 0 {
			d.RouteWeight = d.RolloutPercent
		}
		out = append(out, &d)

	}
	return out
}

// DeleteDeployment removes one deployment record after its VM pool has been stopped.
func (s *Store) DeleteDeployment(projectID, id string) error {
	_, err := s.pool.Exec(context.Background(), `DELETE FROM deployments WHERE project_id = $1 AND id = $2`, projectID, id)
	return err
}

// GetDeployment returns one deployment by id within a project.
func (s *Store) GetDeployment(projectID, id string) (*types.Deployment, bool) {
	for _, d := range s.ListDeployments(projectID) {
		if d.ID == id {
			return d, true
		}
	}
	return nil, false
}

// AllChecksPassed is true when a deployment has checks and every one has
// status "passed"; a deployment with no checks trivially passes.
func AllChecksPassed(d *types.Deployment) bool {
	if len(d.Checks) == 0 {
		return true
	}
	for _, c := range d.Checks {
		if c.Status != "passed" {
			return false
		}
	}
	return true
}

// --- Secrets (per project, stored encrypted) ---

func (s *Store) PutSecret(sec *types.Secret) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO secrets (id, project_id, name, value_encrypted, created_at, updated_at)
		VALUES ($1,$2,$3,$4, now(), now())
		ON CONFLICT (project_id, name) DO UPDATE SET value_encrypted=EXCLUDED.value_encrypted, updated_at=now()`,
		sec.ID, nullableStr(sec.ProjectID), sec.Name, sec.ValueEncrypted)
	return err
}

func (s *Store) ListSecrets(projectID string) []*types.Secret {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, project_id, name, value_encrypted FROM secrets WHERE project_id = $1 ORDER BY name`,
		projectID)
	if err != nil {
		log.Printf("store: list secrets for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Secret, 0)
	for rows.Next() {
		var sec types.Secret
		if err := rows.Scan(&sec.ID, &sec.ProjectID, &sec.Name, &sec.ValueEncrypted); err != nil {
			continue
		}
		out = append(out, &sec)
	}
	return out
}

func (s *Store) DeleteSecret(id string) error {
	_, err := s.pool.Exec(context.Background(), `DELETE FROM secrets WHERE id = $1`, id)
	return err
}

// --- Golden images (direct Firecracker rootfs + kernel manifests) ---

func (s *Store) PutGoldenImage(gi *types.GoldenImage) error {
	ports := gi.Ports
	if ports == nil {
		ports = []types.Port{}
	}
	env := gi.Env
	if env == nil {
		env = map[string]string{}
	}
	tags := gi.Tags
	if tags == nil {
		tags = []string{}
	}
	kind := gi.Kind
	if kind == "" {
		kind = "custom"
	}
	arch := gi.Architecture
	if arch == "" {
		arch = "x86_64"
	}
	status := gi.Status
	if status == "" {
		status = "unknown"
	}
	version := gi.Version
	if version == "" {
		version = "v1"
	}
	data := *gi
	data.Ports, data.Env, data.Tags, data.Version = ports, env, tags, version
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO golden_images (id, name, image, description, vcpus, mem_mib, ports, env, tags, logo, version, kind, architecture, rootfs_path, kernel_path, rootfs_sha256, kernel_sha256, status, validated_at, data, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20, now())
		ON CONFLICT (name) DO UPDATE SET image=EXCLUDED.image, description=EXCLUDED.description,
			vcpus=EXCLUDED.vcpus, mem_mib=EXCLUDED.mem_mib, ports=EXCLUDED.ports,
			env=EXCLUDED.env, tags=EXCLUDED.tags, logo=EXCLUDED.logo, version=EXCLUDED.version,
			kind=EXCLUDED.kind, architecture=EXCLUDED.architecture, rootfs_path=EXCLUDED.rootfs_path,
			kernel_path=EXCLUDED.kernel_path, rootfs_sha256=EXCLUDED.rootfs_sha256,
			kernel_sha256=EXCLUDED.kernel_sha256, status=EXCLUDED.status,
				validated_at=EXCLUDED.validated_at, data=EXCLUDED.data`,
		gi.ID, gi.Name, gi.Image, gi.Description, gi.VCPUs, gi.MemMiB,
		string(mustJSON(ports)), string(mustJSON(env)), tags, gi.Logo, version,
		kind, arch, gi.Rootfs, gi.Kernel, gi.RootfsSHA256, gi.KernelSHA256, status, gi.ValidatedAt,
		string(mustJSON(&data)))
	return err
}

func (s *Store) ListGoldenImages() []*types.GoldenImage {
	rows, err := s.pool.Query(context.Background(), `SELECT data, kind, architecture, rootfs_path, kernel_path, rootfs_sha256, kernel_sha256, status, validated_at FROM golden_images ORDER BY name`)
	if err != nil {
		log.Printf("store: list golden images: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.GoldenImage, 0)
	for rows.Next() {
		var data []byte
		var kind, arch, rootfs, kernel, rootfsSHA, kernelSHA, status string
		var validatedAt *time.Time
		if err := rows.Scan(&data, &kind, &arch, &rootfs, &kernel, &rootfsSHA, &kernelSHA, &status, &validatedAt); err != nil {
			continue
		}
		var gi types.GoldenImage
		if err := json.Unmarshal(data, &gi); err != nil {
			continue
		}
		gi.Kind, gi.Architecture, gi.Rootfs, gi.Kernel = kind, arch, rootfs, kernel
		gi.RootfsSHA256, gi.KernelSHA256, gi.Status, gi.ValidatedAt = rootfsSHA, kernelSHA, status, validatedAt
		out = append(out, &gi)
	}
	return out
}

func (s *Store) DeleteGoldenImage(id string) error {
	_, err := s.pool.Exec(context.Background(), `DELETE FROM golden_images WHERE id = $1`, id)
	return err
}

// --- Metrics + health events ---

func (s *Store) AddMetric(m *types.MetricSample) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO metrics_samples (vm_id, metric, value, ts) VALUES ($1,$2,$3,$4)`,
		nullableStr(m.VMID), m.Metric, m.Value, m.TS)
	return err
}

func (s *Store) ListMetrics(vmID string, limit int) []*types.MetricSample {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT vm_id, metric, value, ts FROM metrics_samples WHERE vm_id = $1 ORDER BY ts DESC LIMIT $2`,
		vmID, limit)
	if err != nil {
		log.Printf("store: list metrics for %s: %v", vmID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.MetricSample, 0)
	for rows.Next() {
		var m types.MetricSample
		if err := rows.Scan(&m.VMID, &m.Metric, &m.Value, &m.TS); err != nil {
			continue
		}
		out = append(out, &m)
	}
	return out
}

func (s *Store) AddHealthEvent(e *types.HealthEvent) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO health_events (vm_id, project_id, status, detail, ts) VALUES ($1,$2,$3,$4,$5)`,
		nullableStr(e.VMID), nullableStr(e.ProjectID), e.Status, e.Detail, e.TS)
	return err
}

func (s *Store) ListHealthEvents(projectID string, limit int) []*types.HealthEvent {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT vm_id, project_id, status, detail, ts FROM health_events
		WHERE project_id = $1 ORDER BY ts DESC LIMIT $2`, projectID, limit)
	if err != nil {
		log.Printf("store: list health events for %s: %v", projectID, err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.HealthEvent, 0)
	for rows.Next() {
		var e types.HealthEvent
		if err := rows.Scan(&e.VMID, &e.ProjectID, &e.Status, &e.Detail, &e.TS); err != nil {
			continue
		}
		out = append(out, &e)
	}
	return out
}

// --- Users ---

// SeededAdminUsername is the migration-created principal name. It is a data
// identity, not an authorization bypass; every request still resolves through
// its users/roles/role_permissions rows.
const SeededAdminUsername = "admin"

// EnsureSeededAdmin initializes the migration-seeded admin row exactly once.
// Existing password hashes are never overwritten. A blank password requires a
// one-time environment secret so the database remains the authority.
func (s *Store) EnsureSeededAdmin(password string) error {
	u, ok := s.GetUserByUsername(SeededAdminUsername)
	if !ok {
		return fmt.Errorf("seeded admin user %q is missing; run database migrations", SeededAdminUsername)
	}
	if u.PasswordHash != "" {
		return nil
	}
	if err := auth.ValidateBootstrapPassword(password); err != nil {
		return fmt.Errorf("seeded admin password is not initialized: %w", err)
	}
	salt, err := auth.NewSalt()
	if err != nil {
		return fmt.Errorf("generate seeded admin salt: %w", err)
	}
	_, err = s.pool.Exec(context.Background(), `
		UPDATE users SET password_hash = $1, salt = $2 WHERE username = $3 AND password_hash = ''`,
		auth.HashPassword(password, salt), salt, SeededAdminUsername)
	if err != nil {
		return fmt.Errorf("initialize seeded admin password: %w", err)
	}
	return nil
}

func (s *Store) PutUser(u *types.User) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO users (id, username, role, email, notify_opt_in, password_hash, salt, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (username) DO UPDATE SET role=EXCLUDED.role,
			email=EXCLUDED.email, notify_opt_in=EXCLUDED.notify_opt_in,
			password_hash=EXCLUDED.password_hash, salt=EXCLUDED.salt`,
		u.ID, u.Username, u.Role, u.Email, u.NotifyOptIn, u.PasswordHash, u.Salt)
	if err != nil {
		log.Printf("store: put user %s: %v", u.Username, err)
	}
}

func (s *Store) ListUsers() []*types.User {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, username, role, COALESCE(email,''), COALESCE(notify_opt_in,false) FROM users ORDER BY created_at`)
	if err != nil {
		log.Printf("store: list users: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.User, 0)
	for rows.Next() {
		var u types.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.NotifyOptIn); err != nil {
			continue
		}
		out = append(out, &u)
	}
	return out
}

func (s *Store) GetUserByUsername(username string) (*types.User, bool) {
	var u types.User
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id, username, role, COALESCE(email,''), COALESCE(notify_opt_in,false), password_hash, salt
		 FROM users WHERE username = $1`, username).
		Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.NotifyOptIn, &u.PasswordHash, &u.Salt); err != nil {
		return nil, false
	}
	return &u, true
}

func (s *Store) GetUserByID(id string) (*types.User, bool) {
	var u types.User
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id, username, role, COALESCE(email,''), COALESCE(notify_opt_in,false), password_hash, salt
		 FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.NotifyOptIn, &u.PasswordHash, &u.Salt); err != nil {
		return nil, false
	}
	return &u, true
}

// GetUserByEmail resolves a user by email address (case-insensitive). Org
// transfer names its new owner by email while orgs.owner_id stores a user ID,
// so the caller must resolve through here — storing the raw address is the bug
// documented above handleOrgTransfer.
func (s *Store) GetUserByEmail(email string) (*types.User, bool) {
	var u types.User
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id, username, role, COALESCE(email,''), COALESCE(notify_opt_in,false), password_hash, salt
		 FROM users WHERE email <> '' AND lower(email) = lower($1)`, email).
		Scan(&u.ID, &u.Username, &u.Role, &u.Email, &u.NotifyOptIn, &u.PasswordHash, &u.Salt); err != nil {
		return nil, false
	}
	return &u, true
}

func (s *Store) DeleteUser(id string) {
	if _, err := s.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id); err != nil {
		log.Printf("store: delete user %s: %v", id, err)
	}
}

// --- Servers (Phase-8 scaffold) ---

const serverCols = `id, hostname, COALESCE(address,''), COALESCE(status,'registered'),
	COALESCE(vcpus,0), COALESCE(mem_mib,0), COALESCE(os,''), COALESCE(arch,''),
	COALESCE(version,''), COALESCE(projects,0), COALESCE(vms,0), last_seen`

func (s *Store) PutServer(srv *types.Server) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO servers (id, hostname, address, status, registered_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (id) DO UPDATE SET hostname=EXCLUDED.hostname, address=EXCLUDED.address, status=EXCLUDED.status`,
		srv.ID, srv.Name, srv.Address, srv.Status)
	if err != nil {
		log.Printf("store: put server %s: %v", srv.ID, err)
	}
}

// scanServer scans one row returned in serverCols order.
func scanServer(row pgx.Row) (*types.Server, error) {
	var srv types.Server
	var lastSeen *time.Time
	if err := row.Scan(&srv.ID, &srv.Name, &srv.Address, &srv.Status, &srv.VCPUs,
		&srv.MemMiB, &srv.OS, &srv.Arch, &srv.Version, &srv.Projects, &srv.VMs, &lastSeen); err != nil {
		return nil, err
	}
	if lastSeen != nil {
		srv.LastSeen = *lastSeen
	}
	return &srv, nil
}

func (s *Store) GetServer(id string) (*types.Server, bool) {
	srv, err := scanServer(s.pool.QueryRow(context.Background(),
		`SELECT `+serverCols+` FROM servers WHERE id = $1`, id))
	if err != nil {
		return nil, false
	}
	return srv, true
}

func (s *Store) ListServers() []*types.Server {
	rows, err := s.pool.Query(context.Background(),
		`SELECT `+serverCols+` FROM servers ORDER BY registered_at`)
	if err != nil {
		log.Printf("store: list servers: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Server, 0)
	for rows.Next() {
		srv, serr := scanServer(rows)
		if serr != nil {
			continue
		}
		out = append(out, srv)
	}
	return out
}

// ApplyHeartbeat updates a worker node's live health fields and stamps
// last_seen. Returns false when the node is not registered.
func (s *Store) ApplyHeartbeat(h *types.ServerHeartbeat) bool {
	res, err := s.pool.Exec(context.Background(), `
		UPDATE servers SET address=$2, status=$3, vcpus=$4, mem_mib=$5,
			os=$6, arch=$7, version=$8, projects=$9, vms=$10, last_seen=now()
		WHERE id=$1`, h.ID, h.Address, h.Status, h.VCPUs, h.MemMiB, h.OS, h.Arch, h.Version, h.Projects, h.VMs)
	if err != nil {
		log.Printf("store: heartbeat %s: %v", h.ID, err)
		return false
	}
	return res.RowsAffected() > 0
}

func (s *Store) DeleteServer(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM servers WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete server %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// --- Traffic ring buffer (in-memory only) ---

func (s *Store) AddTraffic(vmID string, e *types.TrafficEntry) {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	buf := append(s.traffic[vmID], e)
	if len(buf) > trafficRingSize {
		buf = buf[len(buf)-trafficRingSize:]
	}
	s.traffic[vmID] = buf
	s.persistTraffic(vmID, e)
}

// persistTraffic writes a traffic entry to the durable traffic_logs table
// best-effort and non-blocking, so analytics survive restarts. Errors are
// logged once at low volume, never propagated to the hot path.
func (s *Store) persistTraffic(vmID string, e *types.TrafficEntry) {
	if s.pool == nil || e == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		method := e.Method
		if method == "" {
			method = "GET"
		}
		_, err := s.pool.Exec(ctx, `
			INSERT INTO traffic_logs (vm_id, project_id, method, host, path, status, duration_ms, remote_ip, ts)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			nullableStr(vmID), nullableStr(e.ProjectID), method, e.Host, e.Path, e.Status, e.DurationMS, e.RemoteIP, e.Timestamp)
		if err != nil {
			log.Printf("store: persist traffic: %v", err)
		}
	}()
}

// ListTrafficLogs returns durable traffic rows for a project (newest first),
// used by the analytics endpoints when a ring has rolled over.
func (s *Store) ListTrafficLogs(projectID string, limit int) []*types.TrafficEntry {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.pool.Query(context.Background(), `
		SELECT method, host, path, status, duration_ms, remote_ip, ts FROM traffic_logs
		WHERE project_id = $1 ORDER BY ts DESC LIMIT $2`,
		nullableStr(projectID), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]*types.TrafficEntry, 0, limit)
	for rows.Next() {
		var e types.TrafficEntry
		if err := rows.Scan(&e.Method, &e.Host, &e.Path, &e.Status, &e.DurationMS, &e.RemoteIP, &e.Timestamp); err != nil {
			continue
		}
		out = append(out, &e)
	}
	return out
}

// UpsertDailyAnalytics increments the per-day request/bandwidth counters so the
// analytics endpoints can show durable historical usage beyond the ring.
func (s *Store) UpsertDailyAnalytics(projectID string, requests, bandwidth, invocations int) {
	if s.pool == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO analytics_daily (project_id, day, requests, bandwidth, invocations)
			VALUES ($1, CURRENT_DATE, $2, $3, $4)
			ON CONFLICT (project_id, day) DO UPDATE SET
				requests=analytics_daily.requests + EXCLUDED.requests,
				bandwidth=analytics_daily.bandwidth + EXCLUDED.bandwidth,
				invocations=analytics_daily.invocations + EXCLUDED.invocations`,
			nullableStr(projectID), requests, bandwidth, invocations)
	}()
}

func (s *Store) ListTraffic(vmID string, limit int) []*types.TrafficEntry {
	s.trafficMu.RLock()
	defer s.trafficMu.RUnlock()
	buf := s.traffic[vmID]
	if limit <= 0 || limit > len(buf) {
		limit = len(buf)
	}
	start := len(buf) - limit
	out := make([]*types.TrafficEntry, limit)
	copy(out, buf[start:])
	return out
}

// ClearTraffic empties every VM's in-memory traffic ring (DELETE /traffic).
func (s *Store) ClearTraffic() {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	for id := range s.traffic {
		delete(s.traffic, id)
	}
}

// ClearTrafficFor empties the traffic ring for a specific set of VMs, scoping
// a cache purge to its own project (never wipes another tenant's analytics).
func (s *Store) ClearTrafficFor(vmIDs []string) {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	for _, id := range vmIDs {
		delete(s.traffic, id)
	}
}

// ClearTrafficForPath removes traffic-ring entries matching path for a
// project's VMs and deletes their durable traffic_logs. Backs the scoped
// path-level cache purge (POST /projects/{id}/cache/purge/path); unlike
// ClearTrafficFor it keeps everything that is not that one path.
func (s *Store) ClearTrafficForPath(projectID, path string) (removed int) {
	proj, ok := s.GetProject(projectID)
	if !ok {
		return 0
	}
	s.trafficMu.Lock()
	for _, vmID := range proj.VMIDs {
		buf := s.traffic[vmID]
		k := buf[:0]
		for _, e := range buf {
			if e.Path == path {
				removed++
				continue
			}
			k = append(k, e)
		}
		if len(k) == 0 {
			delete(s.traffic, vmID)
		} else {
			s.traffic[vmID] = k
		}
	}
	s.trafficMu.Unlock()
	if s.pool != nil {
		if _, err := s.pool.Exec(context.Background(),
			`DELETE FROM traffic_logs WHERE project_id=$1 AND path=$2`, projectID, path); err == nil {
			// durable rows removed best-effort; ring count is the returned figure
		}
	}
	return removed
}

// --- Web-vitals ring buffer (in-memory only) ---

// AddVital records a browser-reported Core Web Vital beacon.
func (s *Store) AddVital(v *types.WebVital) {
	if v == nil || v.ProjectID == "" {
		return
	}
	s.vitalsMu.Lock()
	defer s.vitalsMu.Unlock()
	buf := append(s.vitals[v.ProjectID], v)
	if len(buf) > 500 {
		buf = buf[len(buf)-500:]
	}
	s.vitals[v.ProjectID] = buf
}

// ListVitals returns the last n web-vitals beacons for a project, newest-last.
func (s *Store) ListVitals(projectID string, limit int) []*types.WebVital {
	s.vitalsMu.RLock()
	defer s.vitalsMu.RUnlock()
	buf := s.vitals[projectID]
	if limit <= 0 || limit > len(buf) {
		limit = len(buf)
	}
	start := len(buf) - limit
	out := make([]*types.WebVital, limit)
	copy(out, buf[start:])
	return out
}

// --- Log ring buffer (in-memory only) ---

func (s *Store) AppendLog(vmID, line string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	buf := append(s.logs[vmID], line)
	if len(buf) > logRingSize {
		buf = buf[len(buf)-logRingSize:]
	}
	s.logs[vmID] = buf
}

func (s *Store) TailLogs(vmID string, n int) []string {
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	buf := s.logs[vmID]
	if n <= 0 || n > len(buf) {
		n = len(buf)
	}
	start := len(buf) - n
	out := make([]string, n)
	copy(out, buf[start:])
	return out
}

// --- Global daemon (audit) log ring ---

func (s *Store) AppendDaemonLog(line string) {
	s.logMu.Lock()
	s.daemonLogs = append(s.daemonLogs, line)
	if len(s.daemonLogs) > daemonLogRingSize {
		s.daemonLogs = s.daemonLogs[len(s.daemonLogs)-daemonLogRingSize:]
	}
	s.logMu.Unlock()

	// Durable audit trail (best-effort; never blocks the control plane).
	if s.pool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(ctx, `INSERT INTO daemon_logs (line) VALUES ($1)`, line)
	}
}

func (s *Store) TailDaemonLogs(n int) []string {
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if n <= 0 || n > len(s.daemonLogs) {
		n = len(s.daemonLogs)
	}
	return append([]string(nil), s.daemonLogs[len(s.daemonLogs)-n:]...)
}

// Ping verifies the PostgreSQL connection is alive (used by /healthz).
func (s *Store) Ping(ctx context.Context) error {
	if s.pool == nil {
		return fmt.Errorf("database pool not initialized")
	}
	return s.pool.Ping(ctx)
}

// PutFeedback persists a user feedback submission.
func (s *Store) PutFeedback(f *types.Feedback) {
	if s.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO feedback (id, subject, message, category, username, project_id)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		f.ID, f.Subject, f.Message, f.Category, f.Username, f.ProjectID)
}

// ListFeedback returns recent feedback submissions, newest first (limit bound).
func (s *Store) ListFeedback(limit int) []*types.Feedback {
	if s.pool == nil {
		return nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT id, subject, message, category, username, project_id, created_at
		FROM feedback ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		log.Printf("store: list feedback: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Feedback, 0, 16)
	for rows.Next() {
		var f types.Feedback
		if err := rows.Scan(&f.ID, &f.Subject, &f.Message, &f.Category, &f.Username, &f.ProjectID, &f.CreatedAt); err != nil {
			continue
		}
		out = append(out, &f)
	}
	return out
}

func statusFromProject(p *types.Project) string {
	switch {
	case len(p.VMIDs) == 0:
		return "pending"
	case p.Source == "compose":
		return "deploying"
	default:
		return "running"
	}
}

// --- 0011: full-PaaS tables (stacks, api_keys, volumes, alerts, hooks, crons,
// drains, redirects, firewall, environments, settings, members, builds, networks) ---

// ---- Stacks (docker-compose projects: each service is its own project) ----

func (s *Store) PutStack(st *types.Stack) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO stacks (id, name, org_id, source, compose_yaml) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, org_id=EXCLUDED.org_id,
		source=EXCLUDED.source, compose_yaml=EXCLUDED.compose_yaml`,
		st.ID, st.Name, nullStr(st.OrgID), st.Source, st.ComposeYAML)
	if err != nil {
		log.Printf("store: put stack %s: %v", st.ID, err)
	}
}

func (s *Store) ListStacks() []*types.Stack {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, name, COALESCE(org_id::text,''), source, compose_yaml FROM stacks ORDER BY created_at`)
	if err != nil {
		log.Printf("store: list stacks: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Stack, 0)
	for rows.Next() {
		var st types.Stack
		if err := rows.Scan(&st.ID, &st.Name, &st.OrgID, &st.Source, &st.ComposeYAML); err != nil {
			continue
		}
		out = append(out, &st)
	}
	return out
}

func (s *Store) GetStack(id string) (*types.Stack, bool) {
	var st types.Stack
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, name, COALESCE(org_id::text,''), source, compose_yaml FROM stacks WHERE id = $1`, id).
		Scan(&st.ID, &st.Name, &st.OrgID, &st.Source, &st.ComposeYAML)
	if err != nil {
		return nil, false
	}
	return &st, true
}

func (s *Store) DeleteStack(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM stacks WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete stack %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- API keys ----

func (s *Store) PutAPIKey(k *types.APIKey) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO api_keys (id, user_id, name, token_hash) VALUES ($1,$2,$3,$4)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, token_hash=EXCLUDED.token_hash`,
		k.ID, nullStr(k.UserID), k.Name, k.TokenHash)
	if err != nil {
		log.Printf("store: put api key %s: %v", k.ID, err)
	}
}

func (s *Store) ListAPIKeys(userID string) []*types.APIKey {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, user_id, name, token_hash, created_at, last_used_at FROM api_keys
		 WHERE user_id = $1 ORDER BY created_at`, nullStr(userID))
	if err != nil {
		log.Printf("store: list api keys: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.APIKey, 0)
	for rows.Next() {
		var k types.APIKey
		var lu *time.Time
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenHash, &k.CreatedAt, &lu); err != nil {
			continue
		}
		k.LastUsed = lu
		out = append(out, &k)
	}
	return out
}

func (s *Store) DeleteAPIKey(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete api key %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// DeleteAPIKeyByHash revokes an opaque bearer token without exposing the
// token's database identifier to the API layer.
func (s *Store) DeleteAPIKeyByHash(hash string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM api_keys WHERE token_hash = $1`, hash)
	if err != nil {
		log.Printf("store: delete api key by hash: %v", err)
		return false
	}
	return res.RowsAffected() > 0
}

// GetAPIKeyByHash finds an API key row by its token hash (the lookup the auth
// middleware uses to resolve a bearer token back to a user).
func (s *Store) GetAPIKeyByHash(hash string) (*types.APIKey, bool) {
	var k types.APIKey
	var lu *time.Time
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, user_id::text, name, token_hash, created_at, last_used_at FROM api_keys WHERE token_hash = $1`, hash).
		Scan(&k.ID, &k.UserID, &k.Name, &k.TokenHash, &k.CreatedAt, &lu)
	if err != nil {
		return nil, false
	}
	k.LastUsed = lu
	return &k, true
}

// GetUserByToken resolves a bearer token (via its stored hash) to the user it
// was issued for. Returns ok=false when the token isn't a per-user key.
func (s *Store) GetUserByToken(raw string) (*types.User, bool) {
	k, ok := s.GetAPIKeyByHash(hashOf(raw))
	if !ok || k.UserID == "" || k.UserID == "*" {
		return nil, false
	}
	u, ok := s.GetUserByID(k.UserID)
	if !ok {
		return nil, false
	}
	return u, true
}

// hashOf is a small local sha256-hex helper (mirrors api.hashToken without
// importing the api package).
func hashOf(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ---- Volumes ----

func (s *Store) PutVolume(v *types.Volume) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO volumes (id, project_id, name, size_mib, mount_path, status)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (project_id, name) DO UPDATE SET
			size_mib=EXCLUDED.size_mib, mount_path=EXCLUDED.mount_path, status=EXCLUDED.status`,
		v.ID, nullStr(v.ProjectID), v.Name, v.SizeMiB, v.Path, statusOf(v))
	if err != nil {
		log.Printf("store: put volume %s: %v", v.ID, err)
	}
}

func (s *Store) ListVolumes(projectID string) []*types.Volume {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, size_mib, mount_path, status FROM volumes
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list volumes: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Volume, 0)
	for rows.Next() {
		var v types.Volume
		var status string
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.Name, &v.SizeMiB, &v.Path, &status); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out
}

func (s *Store) GetVolume(id string) (*types.Volume, bool) {
	var v types.Volume
	var status string
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, size_mib, mount_path, status FROM volumes WHERE id = $1`, id).
		Scan(&v.ID, &v.ProjectID, &v.Name, &v.SizeMiB, &v.Path, &status)
	if err != nil {
		return nil, false
	}
	return &v, true
}

func (s *Store) DeleteVolume(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM volumes WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete volume %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

func (s *Store) ResizeVolume(id string, sizeMiB int) bool {
	res, err := s.pool.Exec(context.Background(),
		`UPDATE volumes SET size_mib = $2 WHERE id = $1`, id, sizeMiB)
	if err != nil {
		log.Printf("store: resize volume %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Alerts ----

func (s *Store) PutAlert(al *types.Alert) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO alerts (id, project_id, name, metric, threshold, op, cooldown_s, silenced)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, metric=EXCLUDED.metric,
		threshold=EXCLUDED.threshold, op=EXCLUDED.op, cooldown_s=EXCLUDED.cooldown_s, silenced=EXCLUDED.silenced`,
		al.ID, nullStr(al.ProjectID), al.Name, al.Metric, al.Threshold, al.Op, al.CooldownS, al.Silenced)
	if err != nil {
		log.Printf("store: put alert %s: %v", al.ID, err)
	}
}

func (s *Store) ListAlerts(projectID string) []*types.Alert {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, metric, threshold, op, cooldown_s, silenced FROM alerts
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list alerts: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Alert, 0)
	for rows.Next() {
		var al types.Alert
		if err := rows.Scan(&al.ID, &al.ProjectID, &al.Name, &al.Metric, &al.Threshold, &al.Op, &al.CooldownS, &al.Silenced); err != nil {
			continue
		}
		out = append(out, &al)
	}
	return out
}

func (s *Store) GetAlert(id string) (*types.Alert, bool) {
	var al types.Alert
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, metric, threshold, op, cooldown_s, silenced FROM alerts WHERE id = $1`, id).
		Scan(&al.ID, &al.ProjectID, &al.Name, &al.Metric, &al.Threshold, &al.Op, &al.CooldownS, &al.Silenced)
	if err != nil {
		return nil, false
	}
	return &al, true
}

func (s *Store) DeleteAlert(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM alerts WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete alert %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

func (s *Store) SetAlertSilenced(id string, silenced bool) bool {
	res, err := s.pool.Exec(context.Background(), `UPDATE alerts SET silenced = $2 WHERE id = $1`, id, silenced)
	if err != nil {
		log.Printf("store: silence alert %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Hooks ----

func (s *Store) PutHook(h *types.Hook) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO hooks (id, project_id, name, url, events, active)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, url=EXCLUDED.url,
		events=EXCLUDED.events, active=EXCLUDED.active`,
		h.ID, nullStr(h.ProjectID), h.Name, h.URL, h.Events, h.Active)
	if err != nil {
		log.Printf("store: put hook %s: %v", h.ID, err)
	}
}

func (s *Store) ListHooks(projectID string) []*types.Hook {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, url, events, active FROM hooks
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list hooks: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Hook, 0)
	for rows.Next() {
		var h types.Hook
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.Name, &h.URL, &h.Events, &h.Active); err != nil {
			continue
		}
		out = append(out, &h)
	}
	return out
}

func (s *Store) DeleteHook(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM hooks WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete hook %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Crons ----

func (s *Store) PutCron(c *types.Cron) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO crons (id, project_id, name, schedule, job_image, active)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, schedule=EXCLUDED.schedule,
		job_image=EXCLUDED.job_image, active=EXCLUDED.active`,
		c.ID, nullStr(c.ProjectID), c.Name, c.Schedule, c.JobImage, c.Active)
	if err != nil {
		log.Printf("store: put cron %s: %v", c.ID, err)
	}
}

func (s *Store) ListCrons(projectID string) []*types.Cron {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, project_id::text, name, schedule, job_image, active, last_run_at FROM crons
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list crons: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Cron, 0)
	for rows.Next() {
		var c types.Cron
		var lr *time.Time
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Schedule, &c.JobImage, &c.Active, &lr); err != nil {
			continue
		}
		c.LastRun = lr
		out = append(out, &c)
	}
	return out
}

// ListAllCrons returns every cron in the system regardless of project — used
// by the scheduler to fire jobs across all projects.
func (s *Store) ListAllCrons() []*types.Cron {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, project_id::text, name, schedule, job_image, active, last_run_at FROM crons ORDER BY created_at`)
	if err != nil {
		log.Printf("store: list all crons: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Cron, 0)
	for rows.Next() {
		var c types.Cron
		var lr *time.Time
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Schedule, &c.JobImage, &c.Active, &lr); err != nil {
			continue
		}
		c.LastRun = lr
		out = append(out, &c)
	}
	return out
}

func (s *Store) GetCron(id string) (*types.Cron, bool) {
	var c types.Cron
	var lr *time.Time
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, project_id::text, name, schedule, job_image, active, last_run_at FROM crons WHERE id = $1`, id).
		Scan(&c.ID, &c.ProjectID, &c.Name, &c.Schedule, &c.JobImage, &c.Active, &lr)
	if err != nil {
		return nil, false
	}
	c.LastRun = lr
	return &c, true
}

func (s *Store) DeleteCron(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM crons WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete cron %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

func (s *Store) TouchCron(id string) bool {
	res, err := s.pool.Exec(context.Background(), `UPDATE crons SET last_run_at = now() WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: touch cron %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Drains ----

func (s *Store) PutDrain(d *types.Drain) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO drains (id, project_id, name, endpoint, kind, active)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, endpoint=EXCLUDED.endpoint,
		kind=EXCLUDED.kind, active=EXCLUDED.active`,
		d.ID, nullStr(d.ProjectID), d.Name, d.Endpoint, d.Kind, d.Active)
	if err != nil {
		log.Printf("store: put drain %s: %v", d.ID, err)
	}
}

func (s *Store) ListDrains(projectID string) []*types.Drain {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, endpoint, kind, active FROM drains
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list drains: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Drain, 0)
	for rows.Next() {
		var d types.Drain
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Endpoint, &d.Kind, &d.Active); err != nil {
			continue
		}
		out = append(out, &d)
	}
	return out
}

func (s *Store) DeleteDrain(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM drains WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete drain %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Redirects ----

func (s *Store) PutRedirect(r *types.Redirect) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO redirects (id, project_id, source, target, permanent)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET source=EXCLUDED.source, target=EXCLUDED.target,
		permanent=EXCLUDED.permanent`,
		r.ID, nullStr(r.ProjectID), r.Source, r.Target, r.Permanent)
	if err != nil {
		log.Printf("store: put redirect %s: %v", r.ID, err)
	}
}

func (s *Store) ListRedirects(projectID string) []*types.Redirect {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), source, target, permanent FROM redirects
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list redirects: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Redirect, 0)
	for rows.Next() {
		var r types.Redirect
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Source, &r.Target, &r.Permanent); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out
}

func (s *Store) DeleteRedirect(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM redirects WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete redirect %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Firewall ----

func (s *Store) PutFirewallRule(r *types.FirewallRule) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO firewall_rules (id, project_id, direction, action, proto, ports, source, priority, active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET direction=EXCLUDED.direction, action=EXCLUDED.action,
		proto=EXCLUDED.proto, ports=EXCLUDED.ports, source=EXCLUDED.source,
		priority=EXCLUDED.priority, active=EXCLUDED.active`,
		r.ID, nullStr(r.ProjectID), r.Direction, r.Action, r.Proto, r.Ports, r.Source, r.Priority, r.Active)
	if err != nil {
		log.Printf("store: put firewall rule %s: %v", r.ID, err)
	}
}

func (s *Store) ListFirewallRules(projectID string) []*types.FirewallRule {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), direction, action, proto, ports, source, priority, active
		 FROM firewall_rules WHERE project_id = $1 ORDER BY priority`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list firewall rules: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.FirewallRule, 0)
	for rows.Next() {
		var r types.FirewallRule
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Direction, &r.Action, &r.Proto, &r.Ports, &r.Source, &r.Priority, &r.Active); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out
}

func (s *Store) GetFirewallRule(id string) (*types.FirewallRule, bool) {
	var r types.FirewallRule
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), direction, action, proto, ports, source, priority, active
		 FROM firewall_rules WHERE id = $1`, id).
		Scan(&r.ID, &r.ProjectID, &r.Direction, &r.Action, &r.Proto, &r.Ports, &r.Source, &r.Priority, &r.Active)
	if err != nil {
		return nil, false
	}
	return &r, true
}

func (s *Store) DeleteFirewallRule(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM firewall_rules WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete firewall rule %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Environments ----

func (s *Store) PutEnvironment(e *types.Environment) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO environments (id, project_id, name, branch, url, env_domain)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (project_id, name) DO UPDATE SET branch=EXCLUDED.branch,
		url=EXCLUDED.url, env_domain=EXCLUDED.env_domain`,
		e.ID, nullStr(e.ProjectID), e.Name, e.Branch, e.URL, e.EnvDomain)
	if err != nil {
		log.Printf("store: put environment %s: %v", e.ID, err)
	}
}

func (s *Store) ListEnvironments(projectID string) []*types.Environment {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, branch, url, env_domain FROM environments
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list environments: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Environment, 0)
	for rows.Next() {
		var e types.Environment
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Name, &e.Branch, &e.URL, &e.EnvDomain); err != nil {
			continue
		}
		out = append(out, &e)
	}
	return out
}

func (s *Store) GetEnvironment(id string) (*types.Environment, bool) {
	var e types.Environment
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, branch, url, env_domain FROM environments WHERE id = $1`, id).
		Scan(&e.ID, &e.ProjectID, &e.Name, &e.Branch, &e.URL, &e.EnvDomain)
	if err != nil {
		return nil, false
	}
	return &e, true
}

func (s *Store) DeleteEnvironment(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM environments WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete environment %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Project settings (per-section JSON) ----

func (s *Store) PutProjectSettings(projectID, section string, data map[string]any) {
	b, _ := json.Marshal(data)
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO project_settings (project_id, section, data, updated_at)
		VALUES ($1,$2,$3, now())
		ON CONFLICT (project_id, section) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		projectID, section, b)
	if err != nil {
		log.Printf("store: put project settings %s/%s: %v", projectID, section, err)
	}
}

func (s *Store) GetProjectSettings(projectID, section string) map[string]any {
	var b []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM project_settings WHERE project_id = $1 AND section = $2`, projectID, section).
		Scan(&b)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ---- Project members (Vercel team parity) ----

func (s *Store) PutProjectMember(m *types.ProjectMember) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO project_members (project_id, user_id, role, invited)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role=EXCLUDED.role, invited=EXCLUDED.invited`,
		m.ProjectID, m.UserID, m.Role, m.Invited)
	if err != nil {
		log.Printf("store: put project member %s/%s: %v", m.ProjectID, m.UserID, err)
	}
}

func (s *Store) ListProjectMembers(projectID string) []*types.ProjectMember {
	rows, err := s.pool.Query(context.Background(),
		`SELECT project_id, user_id, role, invited, created_at FROM project_members
		 WHERE project_id = $1 ORDER BY created_at`, projectID)
	if err != nil {
		log.Printf("store: list project members: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.ProjectMember, 0)
	for rows.Next() {
		var m types.ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.Invited, &m.CreatedAt); err != nil {
			continue
		}
		out = append(out, &m)
	}
	return out
}

func (s *Store) DeleteProjectMember(projectID, userID string) bool {
	res, err := s.pool.Exec(context.Background(),
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	if err != nil {
		log.Printf("store: delete project member %s/%s: %v", projectID, userID, err)
		return false
	}
	return res.RowsAffected() > 0
}

// --- RBAC CRUD (roles / permissions / role_permissions) ---

// ListRoles returns every role with its description and policy flags.
func (s *Store) ListRoles() []*types.Role {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, name, description, scope, is_admin, sees_all, is_default, is_system FROM roles ORDER BY id`)
	if err != nil {
		log.Printf("store: list roles: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Role, 0)
	for rows.Next() {
		var r types.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Scope, &r.IsAdmin, &r.SeesAll, &r.IsDefault, &r.IsSystem); err != nil {
			continue
		}
		r.Permissions = s.RolePermissions(r.ID)
		out = append(out, &r)
	}
	return out
}

// GetRole returns a role (with its permission codes) by id.
func (s *Store) GetRole(id string) (*types.Role, bool) {
	var r types.Role
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, name, description, scope, is_admin, sees_all, is_default, is_system FROM roles WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.Description, &r.Scope, &r.IsAdmin, &r.SeesAll, &r.IsDefault, &r.IsSystem)
	if err != nil {
		return nil, false
	}
	r.Permissions = s.RolePermissions(r.ID)
	return &r, true
}

// DefaultRoleID returns the DB-marked default role (onboarding fallback).
// Empty when no default is set — callers must fail explicitly, never invent
// a role name in code.
func (s *Store) DefaultRoleID() string {
	var id string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id FROM roles WHERE is_default ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return ""
	}
	return id
}

// DefaultGlobalRoleID returns the role a brand-new user is given in users.role:
// the seeded default that is NOT an admin role. It deliberately excludes admin
// roles so onboarding can never silently mint a platform administrator, and
// returns "" when nothing suitable is seeded — callers must fail explicitly
// rather than invent a role name (SRS §66.11).
func (s *Store) DefaultGlobalRoleID() string {
	var id string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT id FROM roles WHERE is_default AND NOT is_admin ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return ""
	}
	return id
}

// IsSystemRole reports DB-protected seed roles (not deletable via the API).
func (s *Store) IsSystemRole(id string) bool {
	r, ok := s.GetRole(id)
	return ok && r.IsSystem
}

// RoleSeesAll reports platform-wide project visibility from the DB flag.
func (s *Store) RoleSeesAll(roleID string) bool {
	r, ok := s.GetRole(roleID)
	return ok && (r.SeesAll || r.IsAdmin)
}

// ProjectMemberRoleID resolves the membership role for project creators and
// invitees from the DB default flag (migration 0024: two roles, member is
// default). Empty means no default is seeded — callers fail explicitly.
func (s *Store) ProjectMemberRoleID() string {
	return s.DefaultRoleID()
}

// PutRole inserts or updates a role row.
func (s *Store) PutRole(r *types.Role) {
	if r.ID == "" || r.Name == "" {
		return
	}
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO roles (id, name, description, scope, is_admin, sees_all, is_default, is_system)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, description=EXCLUDED.description,
			scope=EXCLUDED.scope, is_admin=EXCLUDED.is_admin, sees_all=EXCLUDED.sees_all,
			is_default=EXCLUDED.is_default, is_system=EXCLUDED.is_system`,
		r.ID, r.Name, r.Description, r.Scope, r.IsAdmin, r.SeesAll, r.IsDefault, r.IsSystem)
	if err != nil {
		log.Printf("store: put role %s: %v", r.ID, err)
	}
}

// DeleteRole removes a role and its permission mappings.
func (s *Store) DeleteRole(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete role %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ListAllPermissions returns every permission row.
func (s *Store) ListAllPermissions() []*types.Permission {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, name, description FROM permissions ORDER BY id`)
	if err != nil {
		log.Printf("store: list permissions: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Permission, 0)
	for rows.Next() {
		var p types.Permission
		if err := rows.Scan(&p.ID, &p.Name, &p.Description); err != nil {
			continue
		}
		out = append(out, &p)
	}
	return out
}

// AddRolePermission grants one permission to a role (idempotent).
func (s *Store) AddRolePermission(roleID, permission string) {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO role_permissions (role_id, permission_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
		roleID, permission)
	if err != nil {
		log.Printf("store: add permission %s to %s: %v", permission, roleID, err)
	}
}

// RemoveRolePermission revokes one permission from a role (idempotent).
func (s *Store) RemoveRolePermission(roleID, permission string) {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM role_permissions WHERE role_id = $1 AND permission_id = $2`,
		roleID, permission)
	if err != nil {
		log.Printf("store: remove permission %s from %s: %v", permission, roleID, err)
	}
}

// SetRolePermissions replaces a role's permission set in one transaction.
func (s *Store) SetRolePermissions(roleID string, perms []string) error {
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(),
		`DELETE FROM role_permissions WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	for _, p := range perms {
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO role_permissions (role_id, permission_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			roleID, p); err != nil {
			return err
		}
	}
	return tx.Commit(context.Background())
}

// RolePermissions returns the permission codes granted to a role via the
// role_permissions table. Empty for unknown roles.
func (s *Store) RolePermissions(roleID string) []string {
	if roleID == "" {
		return nil
	}
	rows, err := s.pool.Query(context.Background(),
		`SELECT permission_id FROM role_permissions WHERE role_id = $1`, roleID)
	if err != nil {
		log.Printf("store: role permissions for %s: %v", roleID, err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// HasPermission reports whether a user's GLOBAL role grants the permission.
func (s *Store) HasPermission(username, permission string) bool {
	u, ok := s.GetUserByUsername(username)
	if !ok {
		return false
	}
	for _, p := range s.RolePermissions(u.Role) {
		if p == permission {
			return true
		}
	}
	return false
}

// HasProjectPermission reports whether a user's role ON the project (project
// membership → org membership → platform-admin global role) grants the
// permission. Non-members are denied even when their global role is broad:
// tenant isolation must not rest on the caller's own role string.
func (s *Store) HasProjectPermission(projectID, username, permission string) bool {
	role := s.ProjectRoleForUser(projectID, username)
	if role == "" {
		if !s.IsPlatformAdmin(username) {
			return false
		}
		u, ok := s.GetUserByUsername(username)
		if !ok {
			return false
		}
		role = u.Role
	}
	return s.roleGrants(role, permission)
}

// IsPlatformAdmin reports platform-wide access from the DB is_admin flag.
// Platform admins bypass tenancy gates; everyone else needs membership. No
// role name is hardcoded: the seed migration marks the admin role.
func (s *Store) IsPlatformAdmin(username string) bool {
	u, ok := s.GetUserByUsername(username)
	if !ok {
		return false
	}
	r, ok := s.GetRole(u.Role)
	return ok && r.IsAdmin
}

// HasPermissionAnywhere reports a platform-scope permission via org membership
// first (any member org whose role grants it), then the global role. Lets org
// members create/act without requiring a platform-wide global role.
func (s *Store) HasPermissionAnywhere(username, permission string) bool {
	for _, orgID := range s.UserOrgIDs(username) {
		if role := s.OrgRoleForUser(orgID, username); role != "" && s.roleGrants(role, permission) {
			return true
		}
	}
	return s.HasPermission(username, permission)
}

// HasOrgPermission reports whether a user's role in an org grants the
// permission. Non-members are denied unless platform admin (same tenant rule
// as HasProjectPermission).
func (s *Store) HasOrgPermission(orgID, username, permission string) bool {
	role := s.OrgRoleForUser(orgID, username)
	if role == "" {
		if !s.IsPlatformAdmin(username) {
			return false
		}
		u, ok := s.GetUserByUsername(username)
		if !ok {
			return false
		}
		role = u.Role
	}
	return s.roleGrants(role, permission)
}

// roleGrants checks one role against one permission.
func (s *Store) roleGrants(role, permission string) bool {
	if role == "" {
		return false
	}
	for _, p := range s.RolePermissions(role) {
		if p == permission {
			return true
		}
	}
	return false
}

// OrgRoleForUser resolves a user's role in an org from org_members (owner or
// member). Returns "" when the user has no membership row.
func (s *Store) OrgRoleForUser(orgID, username string) string {
	if orgID == "" || username == "" {
		return ""
	}
	u, ok := s.GetUserByUsername(username)
	if !ok {
		return ""
	}
	var role string
	err := s.pool.QueryRow(context.Background(),
		`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2`,
		orgID, u.ID).Scan(&role)
	if err != nil {
		return ""
	}
	return role
}

// ProjectNotifyEmails returns the recipient addresses for project
// notifications: every project member who has set an email AND opted in
// (notify_opt_in). Falls back to org members of the project's org, then to nil
// (the mailer then uses the configured default_to recipient).
func (s *Store) ProjectNotifyEmails(projectID string) []string {
	if projectID == "" {
		return nil
	}
	// Direct project members with an opt-in email.
	rows, err := s.pool.Query(context.Background(), `
		SELECT u.email FROM project_members pm
		JOIN users u ON u.username = pm.user_id
		WHERE pm.project_id = $1 AND u.notify_opt_in = true AND u.email <> ''`,
		projectID)
	if err != nil {
		log.Printf("store: project notify emails: %v", err)
		return nil
	}
	out := emailRowList(rows)
	if len(out) > 0 {
		return out
	}
	// Fall back to org members (via the project's org).
	rows2, err := s.pool.Query(context.Background(), `
		SELECT u.email FROM projects p
		JOIN org_members om ON om.org_id = p.org_id
		JOIN users u ON u.username = om.user_id
		WHERE p.id = $1 AND u.notify_opt_in = true AND u.email <> ''`,
		projectID)
	if err != nil {
		log.Printf("store: org notify emails: %v", err)
		return nil
	}
	return emailRowList(rows2)
}

// emailRowList drains a query whose single column is email.
func emailRowList(rows pgx.Rows) []string {
	defer rows.Close()
	out := make([]string, 0, 8)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil || email == "" {
			continue
		}
		out = append(out, email)
	}
	return out
}

// ProjectRoleForUser resolves a user's role on a project through the PostgreSQL
// RBAC tables — project_members first, then org_members (via the project's org).
// Returns "" when the user has no explicit membership row (callers then fall
// back to the user's global role).
func (s *Store) ProjectRoleForUser(projectID, username string) string {
	if projectID == "" || username == "" {
		return ""
	}
	u, ok := s.GetUserByUsername(username)
	if !ok {
		return ""
	}
	var role string
	// 1. Direct project membership.
	err := s.pool.QueryRow(context.Background(),
		`SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, u.ID).Scan(&role)
	if err == nil && role != "" {
		return role
	}
	// 2. Org membership for the project's org (owner-level covers every project).
	var orgID string
	_ = s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(org_id::text,'') FROM projects WHERE id = $1`, projectID).Scan(&orgID)
	if orgID == "" {
		return ""
	}
	err = s.pool.QueryRow(context.Background(),
		`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2`,
		orgID, u.ID).Scan(&role)
	if err == nil && role != "" {
		return role
	}
	return ""
}

// ---- Builds (git-based build → OCI → microVM) ----

func (s *Store) PutBuild(b *types.Build) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO builds (id, project_id, deployment_id, git_url, branch, git_commit, build_status, image, log)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET git_url=EXCLUDED.git_url, branch=EXCLUDED.branch,
		deployment_id=EXCLUDED.deployment_id, git_commit=EXCLUDED.git_commit,
		build_status=EXCLUDED.build_status, image=EXCLUDED.image, log=EXCLUDED.log`,
		b.ID, nullStr(b.ProjectID), nullableStr(b.DeploymentID), b.GitURL, b.Branch, b.GitCommit, b.BuildStatus, b.Image, b.Log)
	if err != nil {
		log.Printf("store: put build %s: %v", b.ID, err)
	}
}

func (s *Store) ListBuilds(projectID string) []*types.Build {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), COALESCE(deployment_id::text,''), git_url, branch, COALESCE(git_commit,''), build_status, image, log FROM builds
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list builds: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Build, 0)
	for rows.Next() {
		var b types.Build
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.DeploymentID, &b.GitURL, &b.Branch, &b.GitCommit, &b.BuildStatus, &b.Image, &b.Log); err != nil {
			continue
		}
		out = append(out, &b)
	}
	return out
}

func (s *Store) GetBuild(id string) (*types.Build, bool) {
	var b types.Build
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), COALESCE(deployment_id::text,''), git_url, branch, COALESCE(git_commit,''), build_status, image, log FROM builds WHERE id = $1`, id).
		Scan(&b.ID, &b.ProjectID, &b.DeploymentID, &b.GitURL, &b.Branch, &b.GitCommit, &b.BuildStatus, &b.Image, &b.Log)
	if err != nil {
		return nil, false
	}
	return &b, true
}

func (s *Store) DeleteBuild(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM builds WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete build %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// ---- Networks (docker-ecosystem parity) ----

func (s *Store) PutNetwork(n *types.Network) {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO networks (id, project_id, name, cidr, driver)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (project_id, name) DO UPDATE SET cidr=EXCLUDED.cidr, driver=EXCLUDED.driver`,
		n.ID, nullStr(n.ProjectID), n.Name, n.CIDR, n.Driver)
	if err != nil {
		log.Printf("store: put network %s: %v", n.ID, err)
	}
}

func (s *Store) ListNetworks(projectID string) []*types.Network {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, COALESCE(project_id::text,''), name, cidr, driver FROM networks
		 WHERE project_id = $1 ORDER BY created_at`, nullStr(projectID))
	if err != nil {
		log.Printf("store: list networks: %v", err)
		return nil
	}
	defer rows.Close()
	out := make([]*types.Network, 0)
	for rows.Next() {
		var n types.Network
		if err := rows.Scan(&n.ID, &n.ProjectID, &n.Name, &n.CIDR, &n.Driver); err != nil {
			continue
		}
		out = append(out, &n)
	}
	return out
}

func (s *Store) DeleteNetwork(id string) bool {
	res, err := s.pool.Exec(context.Background(), `DELETE FROM networks WHERE id = $1`, id)
	if err != nil {
		log.Printf("store: delete network %s: %v", id, err)
		return false
	}
	return res.RowsAffected() > 0
}

// nullStr converts "" to NULL for nullable columns.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// statusOf extracts a volume status string (kept for the volume table's status column).
func statusOf(v *types.Volume) string {
	if v.Path == "" {
		return "provisioning"
	}
	return "mounted"
}

var _ = pgx.ErrNoRows // keep pgx imported for future raw queries
var _ = time.Now      // keep time imported
