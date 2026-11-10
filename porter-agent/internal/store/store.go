package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"porter/protocol"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Config struct {
	Server         string `json:"server_url"`
	NodeID         string `json:"node_id"`
	CredentialPath string `json:"credential_path"`
}

type JoinAttempt struct {
	Server    string
	StartedAt time.Time
	Outcome   string
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
CREATE TABLE IF NOT EXISTS vms(id TEXT PRIMARY KEY, replica_id TEXT, state TEXT NOT NULL, pid INTEGER NOT NULL DEFAULT 0, socket TEXT NOT NULL DEFAULT '', tap_name TEXT NOT NULL DEFAULT '', guest_ip TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, health TEXT NOT NULL DEFAULT 'checking', error TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL);
CREATE TABLE IF NOT EXISTS command_result_outbox(command_id TEXT NOT NULL, attempt INTEGER NOT NULL CHECK(attempt > 0), result_json TEXT NOT NULL, created_at_ns INTEGER NOT NULL, PRIMARY KEY(command_id,attempt));
CREATE TABLE IF NOT EXISTS enrollment_attempt(id INTEGER PRIMARY KEY CHECK(id=1), server TEXT NOT NULL, started_at TIMESTAMP NOT NULL, outcome TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// This table is only the local node identity pointer. Remove every legacy
	// or unsupported setting, including secrets and installer/runtime paths.
	if _, err := db.Exec(`DELETE FROM config WHERE k NOT IN ('node_id','server_url','credential_path')`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("remove unsupported identity config entries: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SaveConfig(c Config) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.Server == "" || c.NodeID == "" || c.CredentialPath == "" {
		return fmt.Errorf("agent identity config is incomplete")
	}
	if _, err := tx.Exec(`INSERT INTO config(k,v) VALUES('server_url',?),('node_id',?),('credential_path',?)
ON CONFLICT(k) DO UPDATE SET v=excluded.v`, c.Server, c.NodeID, c.CredentialPath); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM config WHERE k NOT IN ('node_id','server_url','credential_path')`); err != nil {
		return err
	}
	return tx.Commit()
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
		case "server_url":
			c.Server = v
		case "node_id":
			c.NodeID = v
		case "credential_path":
			c.CredentialPath = v
		}
	}
	return c, rows.Err()
}

func (s *Store) SaveJoinAttempt(attempt JoinAttempt) error {
	if attempt.Server == "" || attempt.StartedAt.IsZero() || attempt.Outcome == "" {
		return fmt.Errorf("join attempt recovery metadata is incomplete")
	}
	_, err := s.db.Exec(`INSERT INTO enrollment_attempt(id,server,started_at,outcome) VALUES(1,?,?,?)
ON CONFLICT(id) DO UPDATE SET server=excluded.server,started_at=excluded.started_at,outcome=excluded.outcome`,
		attempt.Server, attempt.StartedAt.UTC(), attempt.Outcome)
	return err
}

func (s *Store) UpdateJoinAttemptOutcome(outcome string) error {
	if outcome == "" {
		return fmt.Errorf("join attempt outcome is required")
	}
	result, err := s.db.Exec(`UPDATE enrollment_attempt SET outcome=? WHERE id=1`, outcome)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("join attempt recovery metadata is missing")
	}
	return nil
}

func (s *Store) UpsertVM(v VM) error {
	_, err := s.db.Exec(`INSERT INTO vms(id,replica_id,state,pid,socket,tap_name,guest_ip,port,health,error,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET replica_id=excluded.replica_id,state=excluded.state,pid=excluded.pid,socket=excluded.socket,tap_name=excluded.tap_name,guest_ip=excluded.guest_ip,port=excluded.port,health=excluded.health,error=excluded.error,updated_at=excluded.updated_at`,
		v.ID, v.ReplicaID, v.State, v.PID, v.Socket, v.TapName, v.GuestIP, v.Port, v.Health, v.Error, time.Now().UTC())
	return err
}

func (s *Store) GetVM(id string) (VM, bool) {
	var v VM
	err := s.db.QueryRow(`SELECT id,replica_id,state,pid,socket,tap_name,guest_ip,port,health,error,updated_at FROM vms WHERE id=?`, id).Scan(&v.ID, &v.ReplicaID, &v.State, &v.PID, &v.Socket, &v.TapName, &v.GuestIP, &v.Port, &v.Health, &v.Error, &v.UpdatedAt)
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

// QueueCommandResult durably stores a result before it is sent to the control plane.
// Re-queueing the identical result is harmless; a different result for the same
// command attempt is rejected rather than replacing the first outcome.
func (s *Store) QueueCommandResult(result protocol.CommandResultV2) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("invalid v2 command result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode command result: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO command_result_outbox(command_id,attempt,result_json,created_at_ns) VALUES(?,?,?,?)`,
		result.CommandID, result.Attempt, string(payload), time.Now().UTC().UnixNano()); err != nil {
		return err
	}
	var existing string
	if err := tx.QueryRow(`SELECT result_json FROM command_result_outbox WHERE command_id=? AND attempt=?`, result.CommandID, result.Attempt).Scan(&existing); err != nil {
		return err
	}
	if existing != string(payload) {
		return fmt.Errorf("conflicting outbox result for command %q attempt %d", result.CommandID, result.Attempt)
	}
	return tx.Commit()
}

// PendingCommandResults returns queued results in durable insertion order.
func (s *Store) PendingCommandResults() ([]protocol.CommandResultV2, error) {
	rows, err := s.db.Query(`SELECT result_json FROM command_result_outbox ORDER BY created_at_ns,command_id,attempt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]protocol.CommandResultV2, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var result protocol.CommandResultV2
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return nil, fmt.Errorf("decode pending command result: %w", err)
		}
		if err := result.Validate(); err != nil {
			return nil, fmt.Errorf("pending command result is not a valid v2 lease receipt: %w", err)
		}
		out = append(out, result)
	}
	return out, rows.Err()
}

// AckCommandResult removes a result only after the control plane accepts it.
// A repeated acknowledgement is idempotent.
func (s *Store) AckCommandResult(commandID string, attempt int) error {
	if commandID == "" || attempt <= 0 {
		return fmt.Errorf("command result identity is incomplete")
	}
	_, err := s.db.Exec(`DELETE FROM command_result_outbox WHERE command_id=? AND attempt=?`, commandID, attempt)
	return err
}
