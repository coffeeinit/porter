package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"porter/protocol"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

type Node struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	CPUs         int       `json:"cpus"`
	MemoryMB     int       `json:"memory_mb"`
	DiskMB       int       `json:"disk_mb"`
	CPUsFree     int       `json:"cpus_free"`
	MemoryFreeMB int       `json:"memory_free_mb"`
	DiskFreeMB   int       `json:"disk_free_mb"`
	VMCount      int       `json:"vm_count"`
	State        string    `json:"state"`
	LastSeen     time.Time `json:"last_seen"`
	CreatedAt    time.Time `json:"created_at"`
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type App struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	Name             string    `json:"name"`
	Image            string    `json:"image"`
	Port             int       `json:"port"`
	VCPUs            int       `json:"vcpus"`
	MemoryMB         int       `json:"memory_mb"`
	ReplicasDesired  int       `json:"replicas_desired"`
	AutoscaleEnabled bool      `json:"autoscale_enabled"`
	MinReplicas      int       `json:"min_replicas"`
	MaxReplicas      int       `json:"max_replicas"`
	CPUTarget        int       `json:"cpu_target"`
	IdleTimeoutSec   int       `json:"idle_timeout_sec"`
	ScaleToZero      bool      `json:"scale_to_zero"`
	LastActivityAt   time.Time `json:"last_activity_at"`
	LastCPUPercent   float64   `json:"last_cpu_percent"`
	PersistenceMode  string    `json:"persistence_mode"`
	VolumeSizeMB     int       `json:"volume_size_mb"`
	CreatedAt        time.Time `json:"created_at"`
}

type Deployment struct {
	ID        string    `json:"id"`
	AppID     string    `json:"app_id"`
	Image     string    `json:"image"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Replica struct {
	ID            string    `json:"id"`
	DeploymentID  string    `json:"deployment_id"`
	AppID         string    `json:"app_id"`
	ProjectID     string    `json:"project_id"`
	NodeID        string    `json:"node_id"`
	VMID          string    `json:"vm_id"`
	GuestIP       string    `json:"guest_ip"`
	HealthStatus  string    `json:"health_status"`
	TrafficWeight int       `json:"traffic_weight"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Operation struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	ResourceID string    `json:"resource_id"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Event struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	ResourceID string    `json:"resource_id"`
	Detail     string    `json:"detail"`
	CreatedAt  time.Time `json:"created_at"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateExtra(); err != nil {
		db.Close()
		return nil, err
	}
	s.ensureAutoscaleColumns()
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS nodes(
 id TEXT PRIMARY KEY, name TEXT NOT NULL, os TEXT, arch TEXT,
 cpus INTEGER NOT NULL DEFAULT 0, memory_mb INTEGER NOT NULL DEFAULT 0,
 disk_mb INTEGER NOT NULL DEFAULT 0, cpus_free INTEGER NOT NULL DEFAULT 0,
 memory_free_mb INTEGER NOT NULL DEFAULT 0, disk_free_mb INTEGER NOT NULL DEFAULT 0,
 vm_count INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'ready',
 token_hash TEXT NOT NULL, last_seen TIMESTAMP NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS join_tokens(
 token_hash TEXT PRIMARY KEY, expires_at TIMESTAMP NOT NULL,
 used INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS projects(
 id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
 created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS apps(
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL,
 image TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 8080,
 vcpus INTEGER NOT NULL DEFAULT 1, memory_mb INTEGER NOT NULL DEFAULT 512,
 replicas_desired INTEGER NOT NULL DEFAULT 1, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS deployments(
 id TEXT PRIMARY KEY, app_id TEXT NOT NULL, image TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'deploying', created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS replicas(
 id TEXT PRIMARY KEY, deployment_id TEXT NOT NULL, app_id TEXT NOT NULL,
 project_id TEXT NOT NULL, node_id TEXT NOT NULL DEFAULT '', vm_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS commands(
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL, operation_id TEXT NOT NULL,
 kind TEXT NOT NULL, payload TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'queued',
 created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS operations(
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, resource_id TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'queued', error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS ip_allocations(ip TEXT PRIMARY KEY, node_id TEXT NOT NULL, replica_id TEXT NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS events(
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, resource_id TEXT NOT NULL,
 detail TEXT NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_commands_node ON commands(node_id, state);
CREATE INDEX IF NOT EXISTS idx_replicas_app ON replicas(app_id);
`)
	return err
}

func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomToken(prefix string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

func (s *Store) CreateJoinToken(ctx context.Context, ttl time.Duration) (string, error) {
	raw, err := randomToken("ptr_join_")
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO join_tokens(token_hash, expires_at, created_at) VALUES(?,?,?)`,
		hashToken(raw), time.Now().Add(ttl).UTC(), time.Now().UTC())
	return raw, err
}

func (s *Store) Enroll(ctx context.Context, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	var zero protocol.EnrollResponse
	var expires time.Time
	var used int
	err := s.db.QueryRowContext(ctx,
		`SELECT expires_at, used FROM join_tokens WHERE token_hash=?`, hashToken(req.Token)).Scan(&expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, errors.New("invalid join token")
	}
	if err != nil {
		return zero, err
	}
	if used != 0 || time.Now().After(expires) {
		return zero, errors.New("join token expired or already used")
	}
	nodeID := NewID("node")
	nodeName := strings.TrimSpace(req.Hostname)
	if nodeName == "" {
		nodeName = nodeID
	}
	rawNodeToken, err := randomToken("ptr_node_")
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE join_tokens SET used=1 WHERE token_hash=? AND used=0`, hashToken(req.Token))
	if err != nil {
		return zero, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return zero, errors.New("join token already used")
	}
	now := time.Now().UTC()
	_, err = tx.Exec(`INSERT INTO nodes(id,name,os,arch,cpus,memory_mb,disk_mb,cpus_free,memory_free_mb,disk_free_mb,state,token_hash,last_seen,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nodeID, nodeName, req.OS, req.Arch, req.CPUs, req.MemoryMB, req.DiskMB,
		req.CPUs, req.MemoryMB, req.DiskMB, "ready", hashToken(rawNodeToken), now, now)
	if err != nil {
		return zero, err
	}
	_, _ = tx.Exec(`INSERT INTO events(id,kind,resource_id,detail,created_at) VALUES(?,?,?,?,?)`,
		NewID("evt"), "node.enrolled", nodeID, "agent enrolled: "+nodeName, now)
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return protocol.EnrollResponse{NodeID: nodeID, NodeToken: rawNodeToken}, nil
}

func (s *Store) AuthNode(ctx context.Context, nodeID, token string) bool {
	var h string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM nodes WHERE id=?`, nodeID).Scan(&h); err != nil {
		return false
	}
	return h == hashToken(token)
}

func (s *Store) Heartbeat(ctx context.Context, hb protocol.Heartbeat) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET cpus_free=?, memory_free_mb=?, disk_free_mb=?, vm_count=?, last_seen=?, state='ready' WHERE id=?`,
		hb.CPUsFree, hb.MemoryFreeMB, hb.DiskFreeMB, hb.VMCount, time.Now().UTC(), hb.NodeID)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO metrics(id,node_id,cpus_free,memory_free_mb,disk_free_mb,vm_count,created_at) VALUES(?,?,?,?,?,?,?)`,
			NewID("met"), hb.NodeID, hb.CPUsFree, hb.MemoryFreeMB, hb.DiskFreeMB, hb.VMCount, time.Now().UTC())
	}
	return err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,os,arch,cpus,memory_mb,disk_mb,cpus_free,memory_free_mb,disk_free_mb,vm_count,state,last_seen,created_at FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.OS, &n.Arch, &n.CPUs, &n.MemoryMB, &n.DiskMB,
			&n.CPUsFree, &n.MemoryFreeMB, &n.DiskFreeMB, &n.VMCount, &n.State, &n.LastSeen, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) CreateProject(ctx context.Context, name string) (Project, error) {
	p := Project{ID: NewID("prj"), Name: strings.TrimSpace(name), Status: "active", CreatedAt: time.Now().UTC()}
	if p.Name == "" {
		return p, errors.New("project name required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(id,name,status,created_at) VALUES(?,?,?,?)`,
		p.ID, p.Name, p.Status, p.CreatedAt)
	return p, err
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,status,created_at FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateApp(ctx context.Context, projectID, name, image string, port, vcpus, memoryMB, replicas int) (App, error) {
	if vcpus <= 0 {
		vcpus = 1
	}
	if memoryMB <= 0 {
		memoryMB = 512
	}
	if replicas <= 0 {
		replicas = 1
	}
	a := App{ID: NewID("app"), ProjectID: projectID, Name: name, Image: image, Port: port,
		VCPUs: vcpus, MemoryMB: memoryMB, ReplicasDesired: replicas,
		MinReplicas: 1, MaxReplicas: 5, CPUTarget: 70, IdleTimeoutSec: 300,
		PersistenceMode: "persistent", VolumeSizeMB: 10240,
		LastActivityAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	if a.Name == "" || a.Image == "" {
		return a, errors.New("app name and image required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO apps(id,project_id,name,image,port,vcpus,memory_mb,replicas_desired,autoscale_enabled,min_replicas,max_replicas,cpu_target,idle_timeout_sec,scale_to_zero,last_activity_at,last_cpu_percent,persistence_mode,volume_size_mb,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ProjectID, a.Name, a.Image, a.Port, a.VCPUs, a.MemoryMB, a.ReplicasDesired,
		a.AutoscaleEnabled, a.MinReplicas, a.MaxReplicas, a.CPUTarget, a.IdleTimeoutSec,
		a.ScaleToZero, a.LastActivityAt, a.LastCPUPercent, a.PersistenceMode, a.VolumeSizeMB, a.CreatedAt)
	return a, err
}

func (s *Store) ListApps(ctx context.Context, projectID string) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,image,port,vcpus,memory_mb,replicas_desired,autoscale_enabled,min_replicas,max_replicas,cpu_target,idle_timeout_sec,scale_to_zero,last_activity_at,last_cpu_percent,persistence_mode,volume_size_mb,created_at FROM apps WHERE project_id=? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Image, &a.Port, &a.VCPUs, &a.MemoryMB, &a.ReplicasDesired,
			&a.AutoscaleEnabled, &a.MinReplicas, &a.MaxReplicas, &a.CPUTarget, &a.IdleTimeoutSec, &a.ScaleToZero,
			&a.LastActivityAt, &a.LastCPUPercent, &a.PersistenceMode, &a.VolumeSizeMB, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateDeployment(ctx context.Context, appID, image string, replicas int) (Deployment, error) {
	var d Deployment
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if image == "" {
		_ = tx.QueryRowContext(ctx, `SELECT image FROM apps WHERE id=?`, appID).Scan(&image)
	}
	d = Deployment{ID: NewID("dep"), AppID: appID, Image: image, Status: "deploying", CreatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,app_id,image,status,created_at) VALUES(?,?,?,?,?)`,
		d.ID, d.AppID, d.Image, d.Status, d.CreatedAt); err != nil {
		return d, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET image=?, replicas_desired=? WHERE id=?`, image, replicas, appID); err != nil {
		return d, err
	}
	var persistence string
	var volumeSize int
	_ = tx.QueryRowContext(ctx, `SELECT persistence_mode,volume_size_mb FROM apps WHERE id=?`, appID).Scan(&persistence, &volumeSize)
	if persistence == "persistent" {
		var count int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM volumes WHERE attached_app_id=? AND state='attached'`, appID).Scan(&count)
		if count == 0 {
			if volumeSize <= 0 {
				volumeSize = 10240
			}
			_, _ = tx.ExecContext(ctx, `INSERT INTO volumes(id,project_id,name,size_mb,state,attached_app_id,created_at) VALUES(?,?,?,?,?,?,?)`,
				NewID("vol"), repProjectID(ctx, tx, appID), "data-"+appID, volumeSize, "attached", appID, time.Now().UTC())
		}
	}
	if err := s.ensureReplicasTx(ctx, tx, appID, d.ID, replicas); err != nil {
		return d, err
	}
	if err := tx.Commit(); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Store) ScaleApp(ctx context.Context, appID string, replicas int) error {
	if replicas < 0 {
		return errors.New("replicas must be >= 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var depID string
	_ = tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE app_id=? ORDER BY created_at DESC LIMIT 1`, appID).Scan(&depID)
	if depID == "" {
		return errors.New("app has no deployment")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET replicas_desired=? WHERE id=?`, replicas, appID); err != nil {
		return err
	}
	if err := s.ensureReplicasTx(ctx, tx, appID, depID, replicas); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureReplicasTx(ctx context.Context, tx *sql.Tx, appID, depID string, desired int) error {
	var projectID string
	var image string
	var vcpus, mem int
	if err := tx.QueryRowContext(ctx, `SELECT project_id,image,vcpus,memory_mb FROM apps WHERE id=?`, appID).Scan(&projectID, &image, &vcpus, &mem); err != nil {
		return err
	}
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM replicas WHERE app_id=? AND state NOT IN ('deleted')`, appID).Scan(&have); err != nil {
		return err
	}
	for i := have; i < desired; i++ {
		rep := Replica{ID: NewID("rep"), DeploymentID: depID, AppID: appID, ProjectID: projectID,
			State: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO replicas(id,deployment_id,app_id,project_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			rep.ID, rep.DeploymentID, rep.AppID, rep.ProjectID, rep.State, rep.CreatedAt, rep.UpdatedAt); err != nil {
			return err
		}
		if err := s.scheduleReplicaTx(ctx, tx, rep, image, vcpus, mem); err != nil {
			return err
		}
	}
	if have > desired {
		rows, err := tx.QueryContext(ctx, `SELECT id,node_id,vm_id FROM replicas WHERE app_id=? AND state NOT IN ('deleted') ORDER BY created_at DESC LIMIT ?`, appID, have-desired)
		if err != nil {
			return err
		}
		type victim struct{ id, nodeID, vmID string }
		var victims []victim
		for rows.Next() {
			var v victim
			_ = rows.Scan(&v.id, &v.nodeID, &v.vmID)
			victims = append(victims, v)
		}
		rows.Close()
		for _, v := range victims {
			now := time.Now().UTC()
			opID := NewID("op")
			_, _ = tx.ExecContext(ctx, `UPDATE replicas SET state='deleting',updated_at=? WHERE id=?`, now, v.id)
			_, _ = tx.ExecContext(ctx, `INSERT INTO operations(id,kind,resource_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
				opID, "vm.delete", v.id, "queued", now, now)
			payload, _ := json.Marshal(protocol.VMApplySpec{VMID: v.vmID})
			_, _ = tx.ExecContext(ctx, `INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?)`,
				NewID("cmd"), v.nodeID, opID, "vm.delete", string(payload), "queued", now)
		}
	}
	return nil
}

func (s *Store) scheduleReplicaTx(ctx context.Context, tx *sql.Tx, rep Replica, image string, vcpus, mem int) error {
	var nodeID string
	var cpusFree, memFree int
	err := tx.QueryRowContext(ctx, `SELECT id,cpus_free,memory_free_mb FROM nodes WHERE state='ready' AND cpus_free>=? AND memory_free_mb>=? ORDER BY memory_free_mb DESC LIMIT 1`, vcpus, mem).Scan(&nodeID, &cpusFree, &memFree)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = tx.ExecContext(ctx, `INSERT INTO events(id,kind,resource_id,detail,created_at) VALUES(?,?,?,?,?)`,
			NewID("evt"), "replica.unscheduled", rep.ID, "no ready node with capacity", time.Now().UTC())
		return nil
	}
	if err != nil {
		return err
	}
	vmID := NewID("vm")
	guestIP, guestMAC, err := allocateIP(ctx, tx, nodeID, rep.ID)
	if err != nil {
		return err
	}
	var volumeID string
	var volumeSize int
	_ = tx.QueryRowContext(ctx, `SELECT id,size_mb FROM volumes WHERE attached_app_id=? AND state='attached' ORDER BY created_at LIMIT 1`, rep.AppID).Scan(&volumeID, &volumeSize)
	var appPort int
	_ = tx.QueryRowContext(ctx, `SELECT port FROM apps WHERE id=?`, rep.AppID).Scan(&appPort)
	var rootfsRef, kernelRef string
	_ = tx.QueryRowContext(ctx, `SELECT rootfs_ref,kernel_ref FROM app_sources WHERE app_id=?`, rep.AppID).Scan(&rootfsRef, &kernelRef)
	spec := protocol.VMApplySpec{VMID: vmID, ReplicaID: rep.ID, ProjectID: rep.ProjectID, DeploymentID: rep.DeploymentID,
		Image: image, KernelPath: kernelRef, RootfsPath: rootfsRef, VCPUs: vcpus, MemoryMB: mem, DiskMB: 1024, Port: appPort,
		Bridge: "porter0", BridgeCIDR: "10.42.0.0/16", GatewayIP: "10.42.0.1",
		GuestIP: guestIP, GuestMAC: guestMAC, TapName: "pt" + strings.ReplaceAll(vmID[len(vmID)-8:], "_", ""),
		VolumeID: volumeID, VolumeSizeMB: volumeSize}
	rows, _ := tx.QueryContext(ctx, `SELECT key,value FROM app_env WHERE app_id=?`, rep.AppID)
	if rows != nil {
		spec.Env = map[string]string{}
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				spec.Env[k] = v
			}
		}
		rows.Close()
	}
	raw, _ := json.Marshal(spec)
	opID := NewID("op")
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE replicas SET node_id=?,vm_id=?,guest_ip=?,state='provisioning',updated_at=? WHERE id=?`, nodeID, vmID, guestIP, now, rep.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,resource_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		opID, "vm.apply", rep.ID, "queued", now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		NewID("cmd"), nodeID, opID, "vm.apply", string(raw), "queued", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET cpus_free=cpus_free-?,memory_free_mb=memory_free_mb-?,vm_count=vm_count+1 WHERE id=?`, vcpus, mem, nodeID); err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `INSERT INTO events(id,kind,resource_id,detail,created_at) VALUES(?,?,?,?,?)`,
		NewID("evt"), "replica.scheduled", rep.ID, "scheduled on "+nodeID, now)
	return nil
}

func (s *Store) ClaimCommands(ctx context.Context, nodeID string) ([]protocol.Command, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,node_id,operation_id,kind,payload,state,created_at FROM commands WHERE node_id=? AND state='queued' ORDER BY created_at`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Command
	for rows.Next() {
		var c protocol.Command
		var payload string
		if err := rows.Scan(&c.ID, &c.NodeID, &c.OperationID, &c.Kind, &payload, &c.State, &c.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &c.Payload)
		out = append(out, c)
	}
	for _, c := range out {
		_, _ = s.db.ExecContext(ctx, `UPDATE commands SET state='sent' WHERE id=?`, c.ID)
	}
	return out, rows.Err()
}

func (s *Store) CompleteCommand(ctx context.Context, r protocol.CommandResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	_ = tx.QueryRowContext(ctx, `SELECT kind FROM commands WHERE id=?`, r.CommandID).Scan(&kind)
	state := "succeeded"
	if !r.Success {
		state = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE commands SET state=? WHERE id=?`, state, r.CommandID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET state=?,error=?,updated_at=? WHERE id=?`, state, r.Error, time.Now().UTC(), r.OperationID); err != nil {
		return err
	}
	if kind == "vm.apply" {
		repState := "running"
		if !r.Success {
			repState = "failed"
		}
		_, _ = tx.ExecContext(ctx, `UPDATE replicas SET state=?,error=?,updated_at=? WHERE id=(SELECT resource_id FROM operations WHERE id=?)`, repState, r.Error, time.Now().UTC(), r.OperationID)
	}
	_, _ = tx.ExecContext(ctx, `INSERT INTO events(id,kind,resource_id,detail,created_at) VALUES(?,?,?,?,?)`,
		NewID("evt"), "operation."+state, r.OperationID, r.Error, time.Now().UTC())
	return tx.Commit()
}

func (s *Store) ListOperations(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,resource_id,state,error,created_at,updated_at FROM operations ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var o Operation
		if err := rows.Scan(&o.ID, &o.Kind, &o.ResourceID, &o.State, &o.Error, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) ListEvents(ctx context.Context) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,resource_id,detail,created_at FROM events ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.ResourceID, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func allocateIP(ctx context.Context, tx *sql.Tx, nodeID, replicaID string) (string, string, error) {
	for i := 2; i < 250; i++ {
		ip := fmt.Sprintf("10.42.0.%d", i)
		var exists int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ip_allocations WHERE ip=?`, ip).Scan(&exists)
		if exists > 0 {
			continue
		}
		mac := fmt.Sprintf("02:00:00:00:00:%02x", i)
		_, err := tx.ExecContext(ctx, `INSERT INTO ip_allocations(ip,node_id,replica_id,created_at) VALUES(?,?,?,?)`, ip, nodeID, replicaID, time.Now().UTC())
		return ip, mac, err
	}
	return "", "", errors.New("no free guest IPs")
}

type Volume struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Name          string    `json:"name"`
	SizeMB        int       `json:"size_mb"`
	State         string    `json:"state"`
	AttachedAppID string    `json:"attached_app_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type SnapshotRecord struct {
	ID        string    `json:"id"`
	VMID      string    `json:"vm_id"`
	StatePath string    `json:"state_path"`
	MemPath   string    `json:"mem_path"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type LogLine struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	VMID      string    `json:"vm_id"`
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"created_at"`
}

type MetricPoint struct {
	ID           string    `json:"id"`
	NodeID       string    `json:"node_id"`
	CPUsFree     int       `json:"cpus_free"`
	MemoryFreeMB int       `json:"memory_free_mb"`
	DiskFreeMB   int       `json:"disk_free_mb"`
	VMCount      int       `json:"vm_count"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Store) migrateExtra() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS volumes(
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL,
 size_mb INTEGER NOT NULL, state TEXT NOT NULL DEFAULT 'available',
 attached_app_id TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots(
 id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, state_path TEXT NOT NULL,
 mem_path TEXT NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS logs(
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL, vm_id TEXT NOT NULL,
 line TEXT NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_logs_vm ON logs(vm_id, created_at DESC);
CREATE TABLE IF NOT EXISTS metrics(
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL, cpus_free INTEGER NOT NULL,
 memory_free_mb INTEGER NOT NULL, disk_free_mb INTEGER NOT NULL,
 vm_count INTEGER NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_metrics_created ON metrics(created_at DESC);
CREATE TABLE IF NOT EXISTS routes(id TEXT PRIMARY KEY, host TEXT NOT NULL, app_id TEXT NOT NULL, path_prefix TEXT NOT NULL DEFAULT '/', created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS request_logs(id TEXT PRIMARY KEY, host TEXT NOT NULL, path TEXT NOT NULL, method TEXT NOT NULL, status INTEGER NOT NULL, duration_ms INTEGER NOT NULL, bytes INTEGER NOT NULL, replica_id TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_request_logs_created ON request_logs(created_at DESC);
CREATE TABLE IF NOT EXISTS domains(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, app_id TEXT NOT NULL, hostname TEXT NOT NULL UNIQUE, kind TEXT NOT NULL DEFAULT 'production', status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_domains_project ON domains(project_id);
CREATE TABLE IF NOT EXISTS images(id TEXT PRIMARY KEY, name TEXT NOT NULL, source_type TEXT NOT NULL, source_ref TEXT NOT NULL, digest TEXT NOT NULL DEFAULT '', kernel_ref TEXT NOT NULL DEFAULT '', rootfs_ref TEXT NOT NULL DEFAULT '', arch TEXT NOT NULL DEFAULT 'amd64', status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '', size_bytes INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS image_logs(id TEXT PRIMARY KEY, image_id TEXT NOT NULL, line TEXT NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE INDEX IF NOT EXISTS idx_image_logs ON image_logs(image_id, created_at);
CREATE TABLE IF NOT EXISTS app_sources(app_id TEXT PRIMARY KEY, repo TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT 'main', rootfs_ref TEXT NOT NULL DEFAULT '', kernel_ref TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS app_env(id TEXT PRIMARY KEY, app_id TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, secret INTEGER NOT NULL DEFAULT 0, UNIQUE(app_id,key));
`)
	return err
}

func (s *Store) CreateVolume(ctx context.Context, projectID, name string, sizeMB int) (Volume, error) {
	if sizeMB <= 0 {
		sizeMB = 1024
	}
	v := Volume{ID: NewID("vol"), ProjectID: projectID, Name: name, SizeMB: sizeMB, State: "available", CreatedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO volumes(id,project_id,name,size_mb,state,created_at) VALUES(?,?,?,?,?,?)`,
		v.ID, v.ProjectID, v.Name, v.SizeMB, v.State, v.CreatedAt)
	return v, err
}

func (s *Store) ListVolumes(ctx context.Context, projectID string) ([]Volume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,size_mb,state,attached_app_id,created_at FROM volumes WHERE project_id=? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.Name, &v.SizeMB, &v.State, &v.AttachedAppID, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AttachVolume(ctx context.Context, volumeID, appID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE volumes SET attached_app_id=?,state='attached' WHERE id=?`, appID, volumeID)
	return err
}

func (s *Store) RecordSnapshot(ctx context.Context, snap SnapshotRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO snapshots(id,vm_id,state_path,mem_path,size_bytes,created_at) VALUES(?,?,?,?,?,?)`,
		snap.ID, snap.VMID, snap.StatePath, snap.MemPath, snap.SizeBytes, time.Now().UTC())
	return err
}

func (s *Store) ListSnapshots(ctx context.Context, vmID string) ([]SnapshotRecord, error) {
	q := `SELECT id,vm_id,state_path,mem_path,size_bytes,created_at FROM snapshots`
	args := []interface{}{}
	if vmID != "" {
		q += ` WHERE vm_id=?`
		args = append(args, vmID)
	}
	q += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotRecord
	for rows.Next() {
		var x SnapshotRecord
		if err := rows.Scan(&x.ID, &x.VMID, &x.StatePath, &x.MemPath, &x.SizeBytes, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) AppendLogs(ctx context.Context, nodeID, vmID string, lines []string) error {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, _ = s.db.ExecContext(ctx, `INSERT INTO logs(id,node_id,vm_id,line,created_at) VALUES(?,?,?,?,?)`,
			NewID("log"), nodeID, vmID, line, time.Now().UTC())
	}
	return nil
}

func (s *Store) ListLogs(ctx context.Context, vmID string, limit int) ([]LogLine, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT id,node_id,vm_id,line,created_at FROM logs`
	args := []interface{}{}
	if vmID != "" {
		q += ` WHERE vm_id=?`
		args = append(args, vmID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.NodeID, &l.VMID, &l.Line, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ListMetrics(ctx context.Context, limit int) ([]MetricPoint, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,node_id,cpus_free,memory_free_mb,disk_free_mb,vm_count,created_at FROM metrics ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var m MetricPoint
		if err := rows.Scan(&m.ID, &m.NodeID, &m.CPUsFree, &m.MemoryFreeMB, &m.DiskFreeMB, &m.VMCount, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) QueueVMCommand(ctx context.Context, vmID, kind, opKind string, payload interface{}) (string, error) {
	var nodeID, replicaID string
	err := s.db.QueryRowContext(ctx, `SELECT node_id,id FROM replicas WHERE vm_id=? LIMIT 1`, vmID).Scan(&nodeID, &replicaID)
	if err != nil {
		return "", fmt.Errorf("vm not found: %w", err)
	}
	raw, _ := json.Marshal(payload)
	opID := NewID("op")
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,resource_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		opID, opKind, replicaID, "queued", now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		NewID("cmd"), nodeID, opID, kind, string(raw), "queued", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return opID, nil
}

func (s *Store) SetReplicaStateByVM(ctx context.Context, vmID, state, errText string) error {
	health := "unhealthy"
	if state == "running" {
		health = "healthy"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE replicas SET state=?,health_status=?,error=?,updated_at=? WHERE vm_id=?`, state, health, errText, time.Now().UTC(), vmID)
	return err
}

func (s *Store) ensureAutoscaleColumns() {
	cols := []string{
		`ALTER TABLE apps ADD COLUMN autoscale_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE apps ADD COLUMN min_replicas INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE apps ADD COLUMN max_replicas INTEGER NOT NULL DEFAULT 5`,
		`ALTER TABLE apps ADD COLUMN cpu_target INTEGER NOT NULL DEFAULT 70`,
		`ALTER TABLE apps ADD COLUMN idle_timeout_sec INTEGER NOT NULL DEFAULT 300`,
		`ALTER TABLE apps ADD COLUMN scale_to_zero INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE apps ADD COLUMN last_activity_at TIMESTAMP`,
		`ALTER TABLE apps ADD COLUMN last_cpu_percent REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE apps ADD COLUMN persistence_mode TEXT NOT NULL DEFAULT 'persistent'`,
		`ALTER TABLE apps ADD COLUMN volume_size_mb INTEGER NOT NULL DEFAULT 10240`,
		`ALTER TABLE replicas ADD COLUMN guest_ip TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE replicas ADD COLUMN health_status TEXT NOT NULL DEFAULT 'checking'`,
		`ALTER TABLE replicas ADD COLUMN traffic_weight INTEGER NOT NULL DEFAULT 100`,
		`ALTER TABLE request_logs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range cols {
		_, _ = s.db.Exec(q)
	}
}

func (s *Store) GetApp(ctx context.Context, id string) (App, bool) {
	var a App
	err := s.db.QueryRowContext(ctx, `SELECT id,project_id,name,image,port,vcpus,memory_mb,replicas_desired,autoscale_enabled,min_replicas,max_replicas,cpu_target,idle_timeout_sec,scale_to_zero,last_activity_at,last_cpu_percent,persistence_mode,volume_size_mb,created_at FROM apps WHERE id=?`, id).
		Scan(&a.ID, &a.ProjectID, &a.Name, &a.Image, &a.Port, &a.VCPUs, &a.MemoryMB, &a.ReplicasDesired, &a.AutoscaleEnabled, &a.MinReplicas, &a.MaxReplicas, &a.CPUTarget, &a.IdleTimeoutSec, &a.ScaleToZero, &a.LastActivityAt, &a.LastCPUPercent, &a.PersistenceMode, &a.VolumeSizeMB, &a.CreatedAt)
	return a, err == nil
}

func (s *Store) PutAutoscalePolicy(ctx context.Context, appID string, enabled bool, min, max, cpuTarget, idleSec int, scaleToZero bool) error {
	if min < 0 {
		min = 0
	}
	if max < min {
		max = min
	}
	if max == 0 {
		max = 1
	}
	return s.scaleExec(ctx, `UPDATE apps SET autoscale_enabled=?,min_replicas=?,max_replicas=?,cpu_target=?,idle_timeout_sec=?,scale_to_zero=? WHERE id=?`,
		enabled, min, max, cpuTarget, idleSec, scaleToZero, appID)
}

func (s *Store) scaleExec(ctx context.Context, q string, args ...interface{}) error {
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *Store) TouchApp(ctx context.Context, appID string, cpuPercent float64) error {
	return s.scaleExec(ctx, `UPDATE apps SET last_activity_at=?,last_cpu_percent=? WHERE id=?`, time.Now().UTC(), cpuPercent, appID)
}

func (s *Store) ReconcileAutoscale(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,replicas_desired,autoscale_enabled,min_replicas,max_replicas,cpu_target,idle_timeout_sec,scale_to_zero,last_activity_at,last_cpu_percent FROM apps WHERE autoscale_enabled=1 OR scale_to_zero=1`)
	if err != nil {
		return err
	}
	type row struct {
		id                                 string
		desired, min, max, cpuTarget, idle int
		enabled, zero                      bool
		last                               sql.NullTime
		cpu                                float64
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.desired, &r.enabled, &r.min, &r.max, &r.cpuTarget, &r.idle, &r.zero, &r.last, &r.cpu); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		if r.zero && r.last.Valid && time.Since(r.last.Time) > time.Duration(r.idle)*time.Second && r.desired > 0 {
			_ = s.ScaleApp(ctx, r.id, 0)
			continue
		}
		if !r.enabled {
			continue
		}
		if r.cpu > float64(r.cpuTarget) && r.desired < r.max {
			_ = s.ScaleApp(ctx, r.id, r.desired+1)
		} else if r.cpu > 0 && r.cpu < float64(r.cpuTarget)/2 && r.desired > r.min {
			_ = s.ScaleApp(ctx, r.id, r.desired-1)
		}
	}
	return nil
}

func (s *Store) RequeueStaleCommands(ctx context.Context, olderThan time.Time) int {
	res, err := s.db.ExecContext(ctx, `UPDATE commands SET state='queued' WHERE state='sent' AND created_at < ?`, olderThan)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

func (s *Store) ListAllApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,project_id,name,image,port,vcpus,memory_mb,replicas_desired,autoscale_enabled,min_replicas,max_replicas,cpu_target,idle_timeout_sec,scale_to_zero,last_activity_at,last_cpu_percent,persistence_mode,volume_size_mb,created_at FROM apps ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Image, &a.Port, &a.VCPUs, &a.MemoryMB, &a.ReplicasDesired, &a.AutoscaleEnabled, &a.MinReplicas, &a.MaxReplicas, &a.CPUTarget, &a.IdleTimeoutSec, &a.ScaleToZero, &a.LastActivityAt, &a.LastCPUPercent, &a.PersistenceMode, &a.VolumeSizeMB, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func repProjectID(ctx context.Context, tx *sql.Tx, appID string) string {
	var projectID string
	_ = tx.QueryRowContext(ctx, `SELECT project_id FROM apps WHERE id=?`, appID).Scan(&projectID)
	return projectID
}

type Route struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	AppID      string    `json:"app_id"`
	PathPrefix string    `json:"path_prefix"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) CreateRoute(ctx context.Context, host, appID, pathPrefix string) (Route, error) {
	if pathPrefix == "" {
		pathPrefix = "/"
	}
	r := Route{ID: NewID("rt"), Host: host, AppID: appID, PathPrefix: pathPrefix, CreatedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO routes(id,host,app_id,path_prefix,created_at) VALUES(?,?,?,?,?)`, r.ID, r.Host, r.AppID, r.PathPrefix, r.CreatedAt)
	return r, err
}

func (s *Store) ListRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,host,app_id,path_prefix,created_at FROM routes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.Host, &r.AppID, &r.PathPrefix, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RouteForHost(ctx context.Context, host string) (Route, bool) {
	var r Route
	err := s.db.QueryRowContext(ctx, `SELECT id,host,app_id,path_prefix,created_at FROM routes WHERE host=? ORDER BY created_at DESC LIMIT 1`, host).Scan(&r.ID, &r.Host, &r.AppID, &r.PathPrefix, &r.CreatedAt)
	return r, err == nil
}

type ReplicaEndpoint struct {
	ReplicaID string `json:"replica_id"`
	IP        string `json:"-"`
	Weight    int    `json:"weight"`
}

func (s *Store) HealthyReplicasForApp(ctx context.Context, appID string) []ReplicaEndpoint {
	rows, err := s.db.QueryContext(ctx, `SELECT id,guest_ip,traffic_weight FROM replicas WHERE app_id=? AND state='running' AND health_status IN ('healthy','checking') AND guest_ip<>''`, appID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ReplicaEndpoint
	for rows.Next() {
		var e ReplicaEndpoint
		_ = rows.Scan(&e.ReplicaID, &e.IP, &e.Weight)
		out = append(out, e)
	}
	return out
}

func (s *Store) RecordRequest(ctx context.Context, traceID, host, path, method string, status, durationMS, bytes int, replicaID string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO request_logs(id,trace_id,host,path,method,status,duration_ms,bytes,replica_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		NewID("req"), traceID, host, path, method, status, durationMS, bytes, replicaID, time.Now().UTC())
}

func (s *Store) SetAppPersistence(ctx context.Context, appID, mode string, sizeMB int) error {
	if sizeMB <= 0 {
		sizeMB = 10240
	}
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET persistence_mode=?,volume_size_mb=? WHERE id=?`, mode, sizeMB, appID)
	return err
}

type Domain struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	AppID     string    `json:"app_id"`
	Hostname  string    `json:"hostname"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type RequestLog struct {
	ID         string    `json:"id"`
	TraceID    string    `json:"trace_id"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Method     string    `json:"method"`
	Status     int       `json:"status"`
	DurationMS int       `json:"duration_ms"`
	Bytes      int       `json:"bytes"`
	ReplicaID  string    `json:"replica_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) CreateDomain(ctx context.Context, projectID, appID, hostname, kind string) (Domain, error) {
	if kind == "" {
		kind = "production"
	}
	d := Domain{ID: NewID("dom"), ProjectID: projectID, AppID: appID, Hostname: hostname, Kind: kind, Status: "active", CreatedAt: time.Now().UTC()}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO domains(id,project_id,app_id,hostname,kind,status,created_at) VALUES(?,?,?,?,?,?,?)`, d.ID, d.ProjectID, d.AppID, d.Hostname, d.Kind, d.Status, d.CreatedAt); err != nil {
		return d, err
	}
	_, err := s.CreateRoute(ctx, hostname, appID, "/")
	return d, err
}

func (s *Store) ListDomains(ctx context.Context, projectID string) ([]Domain, error) {
	q := `SELECT id,project_id,app_id,hostname,kind,status,created_at FROM domains`
	args := []interface{}{}
	if projectID != "" {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.AppID, &d.Hostname, &d.Kind, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) VerifyDomain(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE domains SET status='active' WHERE id=?`, id)
	return err
}

func (s *Store) ListRequestLogs(ctx context.Context, limit int) ([]RequestLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,trace_id,host,path,method,status,duration_ms,bytes,replica_id,created_at FROM request_logs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		var x RequestLog
		if err := rows.Scan(&x.ID, &x.TraceID, &x.Host, &x.Path, &x.Method, &x.Status, &x.DurationMS, &x.Bytes, &x.ReplicaID, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GatewayStats(ctx context.Context) map[string]interface{} {
	var requests, errors, bytes int64
	var avgMS float64
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(AVG(duration_ms),0), COALESCE(SUM(bytes),0), COALESCE(SUM(CASE WHEN status>=500 THEN 1 ELSE 0 END),0) FROM request_logs`).Scan(&requests, &avgMS, &bytes, &errors)
	return map[string]interface{}{"requests": requests, "avg_ms": avgMS, "bytes": bytes, "errors": errors}
}

func (s *Store) GetNode(ctx context.Context, id string) (Node, bool) {
	var n Node
	err := s.db.QueryRowContext(ctx, `SELECT id,name,os,arch,cpus,memory_mb,disk_mb,cpus_free,memory_free_mb,disk_free_mb,vm_count,state,last_seen,created_at FROM nodes WHERE id=?`, id).
		Scan(&n.ID, &n.Name, &n.OS, &n.Arch, &n.CPUs, &n.MemoryMB, &n.DiskMB, &n.CPUsFree, &n.MemoryFreeMB, &n.DiskFreeMB, &n.VMCount, &n.State, &n.LastSeen, &n.CreatedAt)
	return n, err == nil
}

func (s *Store) ListNodeMetrics(ctx context.Context, nodeID string, limit int) ([]MetricPoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,node_id,cpus_free,memory_free_mb,disk_free_mb,vm_count,created_at FROM metrics WHERE node_id=? ORDER BY created_at DESC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var m MetricPoint
		if err := rows.Scan(&m.ID, &m.NodeID, &m.CPUsFree, &m.MemoryFreeMB, &m.DiskFreeMB, &m.VMCount, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ListNodeVMs(ctx context.Context, nodeID string) ([]Replica, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,deployment_id,app_id,project_id,node_id,vm_id,guest_ip,health_status,traffic_weight,state,error,created_at,updated_at FROM replicas WHERE node_id=? ORDER BY created_at`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replica
	for rows.Next() {
		var r Replica
		if err := rows.Scan(&r.ID, &r.DeploymentID, &r.AppID, &r.ProjectID, &r.NodeID, &r.VMID, &r.GuestIP, &r.HealthStatus, &r.TrafficWeight, &r.State, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListNodeLogs(ctx context.Context, nodeID string, limit int) ([]LogLine, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,node_id,vm_id,line,created_at FROM logs WHERE node_id=? ORDER BY created_at DESC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.NodeID, &l.VMID, &l.Line, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) SetNodeState(ctx context.Context, nodeID, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET state=? WHERE id=?`, state, nodeID)
	return err
}

func (s *Store) CreatePreviewDomain(ctx context.Context, appID string, pr int, baseDomain string) (Domain, error) {
	var projectID, name string
	if err := s.db.QueryRowContext(ctx, `SELECT project_id,name FROM apps WHERE id=?`, appID).Scan(&projectID, &name); err != nil {
		return Domain{}, err
	}
	host := "pr-" + strconv.Itoa(pr) + "-" + slug(name) + "." + baseDomain
	return s.CreateDomain(ctx, projectID, appID, host, "preview")
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

type ImageRecord struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	SourceType string    `json:"source_type"`
	SourceRef  string    `json:"source_ref"`
	Digest     string    `json:"digest"`
	KernelRef  string    `json:"kernel_ref"`
	RootfsRef  string    `json:"rootfs_ref"`
	Arch       string    `json:"arch"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) CreateImage(ctx context.Context, img ImageRecord) (ImageRecord, error) {
	if img.ID == "" {
		img.ID = NewID("img")
	}
	if img.Arch == "" {
		img.Arch = "amd64"
	}
	if img.Status == "" {
		img.Status = "pending"
	}
	if img.KernelRef != "" && img.RootfsRef != "" {
		img.Status = "ready"
	}
	img.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO images(id,name,source_type,source_ref,digest,kernel_ref,rootfs_ref,arch,status,error,size_bytes,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		img.ID, img.Name, img.SourceType, img.SourceRef, img.Digest, img.KernelRef, img.RootfsRef, img.Arch, img.Status, img.Error, img.SizeBytes, img.CreatedAt)
	return img, err
}

func (s *Store) ListImages(ctx context.Context) ([]ImageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,source_type,source_ref,digest,kernel_ref,rootfs_ref,arch,status,error,size_bytes,created_at FROM images ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageRecord
	for rows.Next() {
		var i ImageRecord
		if err := rows.Scan(&i.ID, &i.Name, &i.SourceType, &i.SourceRef, &i.Digest, &i.KernelRef, &i.RootfsRef, &i.Arch, &i.Status, &i.Error, &i.SizeBytes, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) ResizeVolume(ctx context.Context, id string, sizeMB int) error {
	if sizeMB <= 0 {
		return errors.New("size_mb must be positive")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE volumes SET size_mb=? WHERE id=?`, sizeMB, id)
	return err
}

func (s *Store) SetReplicaWeight(ctx context.Context, replicaID string, weight int) error {
	if weight < 0 {
		weight = 0
	}
	if weight > 100 {
		weight = 100
	}
	_, err := s.db.ExecContext(ctx, `UPDATE replicas SET traffic_weight=? WHERE id=?`, weight, replicaID)
	return err
}

func (s *Store) GetSnapshot(ctx context.Context, id string) (SnapshotRecord, bool) {
	var x SnapshotRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,vm_id,state_path,mem_path,size_bytes,created_at FROM snapshots WHERE id=?`, id).Scan(&x.ID, &x.VMID, &x.StatePath, &x.MemPath, &x.SizeBytes, &x.CreatedAt)
	return x, err == nil
}

func (s *Store) SetReplicaHealthByVM(ctx context.Context, vmID, state, health, errText string) error {
	if health == "" {
		health = "unhealthy"
		if state == "running" {
			health = "healthy"
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE replicas SET state=?,health_status=?,error=?,updated_at=? WHERE vm_id=?`, state, health, errText, time.Now().UTC(), vmID)
	return err
}

func (s *Store) AppPort(ctx context.Context, appID string) (int, bool) {
	var port int
	err := s.db.QueryRowContext(ctx, `SELECT port FROM apps WHERE id=?`, appID).Scan(&port)
	return port, err == nil
}

func (s *Store) GetImage(ctx context.Context, id string) (ImageRecord, bool) {
	var i ImageRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,name,source_type,source_ref,digest,kernel_ref,rootfs_ref,arch,status,error,size_bytes,created_at FROM images WHERE id=?`, id).
		Scan(&i.ID, &i.Name, &i.SourceType, &i.SourceRef, &i.Digest, &i.KernelRef, &i.RootfsRef, &i.Arch, &i.Status, &i.Error, &i.SizeBytes, &i.CreatedAt)
	return i, err == nil
}

func (s *Store) UpdateImage(ctx context.Context, id, status, digest, kernelRef, rootfsRef, errText string, size int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE images SET status=?,digest=?,kernel_ref=?,rootfs_ref=?,error=?,size_bytes=? WHERE id=?`, status, digest, kernelRef, rootfsRef, errText, size, id)
	return err
}

func (s *Store) AppendImageLog(ctx context.Context, imageID, line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO image_logs(id,image_id,line,created_at) VALUES(?,?,?,?)`, NewID("ilog"), imageID, line, time.Now().UTC())
}

func (s *Store) ListImageLogs(ctx context.Context, imageID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT line FROM image_logs WHERE image_id=? ORDER BY created_at DESC LIMIT ?`, imageID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	// reverse to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

type AppEnv struct {
	ID     string `json:"id"`
	AppID  string `json:"app_id"`
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret"`
}

func (s *Store) ListAppEnv(ctx context.Context, appID string) ([]AppEnv, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,key,value,secret FROM app_env WHERE app_id=? ORDER BY key`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppEnv
	for rows.Next() {
		var e AppEnv
		if err := rows.Scan(&e.ID, &e.AppID, &e.Key, &e.Value, &e.Secret); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PutAppEnv(ctx context.Context, appID, key, value string, secret bool) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("env key required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_env(id,app_id,key,value,secret) VALUES(?,?,?,?,?)
		ON CONFLICT(app_id,key) DO UPDATE SET value=excluded.value, secret=excluded.secret`, NewID("env"), appID, key, value, secret)
	return err
}

func (s *Store) DeleteAppEnv(ctx context.Context, appID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_env WHERE app_id=? AND key=?`, appID, key)
	return err
}

func (s *Store) ListDeployments(ctx context.Context, appID string) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,image,status,created_at FROM deployments WHERE app_id=? ORDER BY created_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Image, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RollbackApp(ctx context.Context, appID string) (Deployment, error) {
	var previousImage string
	err := s.db.QueryRowContext(ctx, `SELECT image FROM deployments WHERE app_id=? ORDER BY created_at DESC LIMIT 1 OFFSET 1`, appID).Scan(&previousImage)
	if err != nil {
		return Deployment{}, errors.New("no previous deployment to roll back to")
	}
	var desired int
	_ = s.db.QueryRowContext(ctx, `SELECT replicas_desired FROM apps WHERE id=?`, appID).Scan(&desired)
	if desired <= 0 {
		desired = 1
	}
	return s.CreateDeployment(ctx, appID, previousImage, desired)
}

func (s *Store) QueueVolumeResize(ctx context.Context, volumeID string, sizeMB int) (string, error) {
	var appID string
	if err := s.db.QueryRowContext(ctx, `SELECT attached_app_id FROM volumes WHERE id=?`, volumeID).Scan(&appID); err != nil {
		return "", err
	}
	var nodeID, replicaID string
	if err := s.db.QueryRowContext(ctx, `SELECT node_id,id FROM replicas WHERE app_id=? AND state='running' LIMIT 1`, appID).Scan(&nodeID, &replicaID); err != nil {
		return "", errors.New("no running replica for volume")
	}
	payload, _ := json.Marshal(protocol.VolumeResizeSpec{VolumeID: volumeID, SizeMB: sizeMB})
	opID := NewID("op")
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,resource_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`, opID, "volume.resize", replicaID, "queued", now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commands(id,node_id,operation_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?)`, NewID("cmd"), nodeID, opID, "volume.resize", string(payload), "queued", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return opID, nil
}

func (s *Store) BackupTo(dst string) error {
	if dst == "" {
		return errors.New("backup path required")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	_, err := s.db.Exec(`VACUUM INTO '` + strings.ReplaceAll(dst, "'", "''") + `'`)
	return err
}

func VerifyBackup(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) ListAppReplicas(ctx context.Context, appID string) ([]Replica, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,deployment_id,app_id,project_id,node_id,vm_id,guest_ip,health_status,traffic_weight,state,error,created_at,updated_at FROM replicas WHERE app_id=? ORDER BY created_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replica
	for rows.Next() {
		var r Replica
		if err := rows.Scan(&r.ID, &r.DeploymentID, &r.AppID, &r.ProjectID, &r.NodeID, &r.VMID, &r.GuestIP, &r.HealthStatus, &r.TrafficWeight, &r.State, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetDeployment(ctx context.Context, id string) (Deployment, bool) {
	var d Deployment
	err := s.db.QueryRowContext(ctx, `SELECT id,app_id,image,status,created_at FROM deployments WHERE id=?`, id).Scan(&d.ID, &d.AppID, &d.Image, &d.Status, &d.CreatedAt)
	return d, err == nil
}

func (s *Store) SetAppSource(ctx context.Context, appID, repo, branch string) error {
	if branch == "" {
		branch = "main"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_sources(app_id,repo,branch) VALUES(?,?,?) ON CONFLICT(app_id) DO UPDATE SET repo=excluded.repo,branch=excluded.branch`, appID, repo, branch)
	return err
}

func (s *Store) FindAppByRepo(ctx context.Context, repo string) (App, bool) {
	var appID string
	if err := s.db.QueryRowContext(ctx, `SELECT app_id FROM app_sources WHERE repo=? LIMIT 1`, repo).Scan(&appID); err != nil {
		return App{}, false
	}
	return s.GetApp(ctx, appID)
}

func (s *Store) SetAppImage(ctx context.Context, appID, rootfsRef, kernelRef string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_sources(app_id,rootfs_ref,kernel_ref) VALUES(?,?,?) ON CONFLICT(app_id) DO UPDATE SET rootfs_ref=excluded.rootfs_ref,kernel_ref=excluded.kernel_ref`, appID, rootfsRef, kernelRef)
	return err
}

func (s *Store) GetAppSource(ctx context.Context, appID string) (repo, branch, rootfsRef, kernelRef string) {
	_ = s.db.QueryRowContext(ctx, `SELECT repo,branch,rootfs_ref,kernel_ref FROM app_sources WHERE app_id=?`, appID).Scan(&repo, &branch, &rootfsRef, &kernelRef)
	return
}
