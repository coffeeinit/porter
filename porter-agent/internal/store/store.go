package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Config struct {
	Server    string `json:"server"`
	NodeID    string `json:"node_id"`
	NodeToken string `json:"node_token"`
	DataDir   string `json:"data_dir"`
	Kernel    string `json:"kernel"`
	Rootfs    string `json:"rootfs"`
}

type VM struct {
	ID        string    `json:"id"`
	ReplicaID string    `json:"replica_id"`
	State     string    `json:"state"`
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	TapName   string    `json:"tap_name"`
	GuestIP   string    `json:"guest_ip"`
	Port      int       `json:"port"`
	Health    string    `json:"health"`
	Error     string    `json:"error"`
	UpdatedAt time.Time `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS config(k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS vms(id TEXT PRIMARY KEY, replica_id TEXT, state TEXT NOT NULL, pid INTEGER NOT NULL DEFAULT 0, socket TEXT NOT NULL DEFAULT '', tap_name TEXT NOT NULL DEFAULT '', guest_ip TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, health TEXT NOT NULL DEFAULT 'checking', error TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL);`); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SaveConfig(c Config) error {
	_, err := s.db.Exec(`INSERT INTO config(k,v) VALUES('server',?),('node_id',?),('node_token',?),('data_dir',?),('kernel',?),('rootfs',?)
ON CONFLICT(k) DO UPDATE SET v=excluded.v`, c.Server, c.NodeID, c.NodeToken, c.DataDir, c.Kernel, c.Rootfs)
	return err
}

func (s *Store) LoadConfig() (Config, error) {
	var c Config
	rows, err := s.db.Query(`SELECT k,v FROM config`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return c, err
		}
		switch k {
		case "server":
			c.Server = v
		case "node_id":
			c.NodeID = v
		case "node_token":
			c.NodeToken = v
		case "data_dir":
			c.DataDir = v
		case "kernel":
			c.Kernel = v
		case "rootfs":
			c.Rootfs = v
		}
	}
	return c, rows.Err()
}

func (s *Store) UpsertVM(v VM) error {
	_, err := s.db.Exec(`INSERT INTO vms(id,replica_id,state,pid,socket,tap_name,guest_ip,port,health,error,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET replica_id=excluded.replica_id,state=excluded.state,pid=excluded.pid,socket=excluded.socket,tap_name=excluded.tap_name,guest_ip=excluded.guest_ip,port=excluded.port,health=excluded.health,error=excluded.error,updated_at=excluded.updated_at`,
		v.ID, v.ReplicaID, v.State, v.PID, v.Socket, v.TapName, v.GuestIP, v.Port, v.Health, v.Error, time.Now().UTC())
	return err
}

func (s *Store) GetVM(id string) (VM, bool) {
	var v VM
	err := s.db.QueryRow(`SELECT id,replica_id,state,pid,socket,tap_name,error,updated_at FROM vms WHERE id=?`, id).Scan(&v.ID, &v.ReplicaID, &v.State, &v.PID, &v.Socket, &v.TapName, &v.Error, &v.UpdatedAt)
	return v, err == nil
}

func (s *Store) CountVMs() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM vms WHERE state NOT IN ('deleted','failed')`).Scan(&n)
	return n
}

func (s *Store) ListVMs() []VM {
	rows, err := s.db.Query(`SELECT id,replica_id,state,pid,socket,tap_name,guest_ip,port,health,error,updated_at FROM vms`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []VM
	for rows.Next() {
		var v VM
		if err := rows.Scan(&v.ID, &v.ReplicaID, &v.State, &v.PID, &v.Socket, &v.TapName, &v.GuestIP, &v.Port, &v.Health, &v.Error, &v.UpdatedAt); err == nil {
			out = append(out, v)
		}
	}
	return out
}
