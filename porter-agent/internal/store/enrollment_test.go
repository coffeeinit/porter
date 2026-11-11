package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestJoinAttemptHostnameMigratesAndPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "porter-agent.db")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE enrollment_attempt(id INTEGER PRIMARY KEY CHECK(id=1), server TEXT NOT NULL, started_at TIMESTAMP NOT NULL, outcome TEXT NOT NULL)`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO enrollment_attempt(id,server,started_at,outcome) VALUES(1,?,?,?)`, "https://control.example", time.Now().UTC(), "outcome_unknown"); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open and migrate legacy store: %v", err)
	}
	legacyAttempt, found, err := st.JoinAttemptRecovery()
	if err != nil || !found || legacyAttempt.Hostname != "" || legacyAttempt.Outcome != "outcome_unknown" {
		t.Fatalf("migrated legacy attempt = (%+v, %t, %v), want empty hostname and preserved outcome", legacyAttempt, found, err)
	}
	want := JoinAttempt{
		Server: "https://control.example", Hostname: "node-for-recovery",
		StartedAt: time.Date(2026, time.November, 11, 12, 34, 56, 123, time.UTC), Outcome: "submitted",
	}
	if err := st.SaveJoinAttempt(want); err != nil {
		_ = st.Close()
		t.Fatalf("save hostname recovery metadata: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.JoinAttemptRecovery()
	if err != nil || !found || got.Server != want.Server || got.Hostname != want.Hostname || !got.StartedAt.Equal(want.StartedAt) || got.Outcome != want.Outcome {
		t.Fatalf("reopened join recovery metadata = (%+v, %t, %v), want (%+v, true, nil)", got, found, err, want)
	}
}
