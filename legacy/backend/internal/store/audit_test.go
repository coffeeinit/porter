package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// TestClampAuditWindow pins the list paging bounds (default 100, max 500,
// offset never negative) without touching the database.
func TestClampAuditWindow(t *testing.T) {
	cases := []struct {
		inL, inO, outL, outO int
	}{
		{0, 0, auditDefaultLimit, 0},   // zero limit -> default
		{-5, -3, auditDefaultLimit, 0}, // negatives normalized
		{501, 0, auditMaxLimit, 0},     // clamped to max
		{500, 0, 500, 0},               // max passes through
		{7, 9, 7, 9},                   // sane values untouched
	}
	for _, c := range cases {
		l, o := clampAuditWindow(c.inL, c.inO)
		if l != c.outL || o != c.outO {
			t.Errorf("clampAuditWindow(%d,%d) = (%d,%d), want (%d,%d)",
				c.inL, c.inO, l, o, c.outL, c.outO)
		}
	}
}

// TestAppendAuditRecordAndListFilters exercises the structured read path
// against the real audit_events table (migration 0018): exact-match filters,
// filtered total, newest-first order, and NULL-IP round-trip.
func TestAppendAuditRecordAndListFilters(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres audit test")
	}
	s := NewStore(testDSN())
	defer s.Close()

	actor := "wp3-audit-" + uuid.NewString()
	seed := []struct{ action, resource, reqID, ip, outcome string }{
		{"wp3.vm.start", "/vms/a", "req-1", "10.0.0.1", "allowed"},
		{"wp3.vm.start", "/vms/a", "req-2", "", "denied"},
		{"wp3.vm.stop", "/vms/b", "req-3", "10.0.0.2", "failed"},
	}
	for _, r := range seed {
		if err := s.AppendAuditRecord("user", actor, r.action, r.resource, r.reqID, r.ip, r.outcome); err != nil {
			t.Fatalf("AppendAuditRecord: %v", err)
		}
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), "DELETE FROM audit_events WHERE actor_id = $1", actor)
	}()

	// Unfiltered by actor: all three rows, newest first (id DESC tiebreak for
	// identical timestamps).
	evs, total, err := s.ListAuditEvents(AuditFilter{Actor: actor})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if total != 3 || len(evs) != 3 {
		t.Fatalf("expected 3 events (total 3), got len=%d total=%d", len(evs), total)
	}
	if evs[0].RequestID != "req-3" || evs[2].RequestID != "req-1" {
		t.Errorf("expected newest-first order, got %v..%v", evs[0].RequestID, evs[2].RequestID)
	}
	if evs[0].ActorType != "user" || evs[0].ActorID != actor || evs[0].Outcome != "failed" {
		t.Errorf("row fields wrong: %+v", evs[0])
	}
	// IP round-trip: set IP preserved, empty IP stored NULL and read back "".
	if evs[0].IP != "10.0.0.2" {
		t.Errorf("expected ip 10.0.0.2, got %q", evs[0].IP)
	}
	if evs[1].IP != "" {
		t.Errorf("expected empty ip for NULL INET, got %q", evs[1].IP)
	}

	// Action filter narrows rows AND the reported total.
	evs, total, err = s.ListAuditEvents(AuditFilter{Actor: actor, Action: "wp3.vm.start"})
	if err != nil {
		t.Fatalf("ListAuditEvents(action): %v", err)
	}
	if total != 2 || len(evs) != 2 {
		t.Fatalf("action filter: expected 2/2, got len=%d total=%d", len(evs), total)
	}
	for _, e := range evs {
		if e.Action != "wp3.vm.start" || e.ResourceRef != "/vms/a" {
			t.Errorf("action filter leaked row: %+v", e)
		}
	}

	// Resource filter.
	_, total, err = s.ListAuditEvents(AuditFilter{Actor: actor, Resource: "/vms/b"})
	if err != nil || total != 1 {
		t.Fatalf("resource filter: total=%d err=%v, want 1/nil", total, err)
	}

	// Paging: limit applies to rows, not to the filtered total.
	evs, total, err = s.ListAuditEvents(AuditFilter{Actor: actor, Action: "wp3.vm.start", Limit: 1})
	if err != nil {
		t.Fatalf("ListAuditEvents(limit): %v", err)
	}
	if len(evs) != 1 || total != 2 {
		t.Fatalf("limit=1: expected 1 row / total 2, got len=%d total=%d", len(evs), total)
	}

	// No match: empty slice (not nil), zero total.
	evs, total, err = s.ListAuditEvents(AuditFilter{Actor: actor, Action: "wp3.nope"})
	if err != nil || total != 0 || evs == nil || len(evs) != 0 {
		t.Fatalf("no-match: events=%v total=%d err=%v, want [] 0 nil", evs, total, err)
	}
}

// TestEmailVerificationLifecycle covers the hash-only token ledger
// (migration 0037): plain token returned once and never stored, single-use
// consumption, expiry, unknown tokens, and the verified flag flip.
func TestEmailVerificationLifecycle(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres verification test")
	}
	s := NewStore(testDSN())
	defer s.Close()

	userID := uuid.NewString()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO users (id, username, password_hash) VALUES ($1, $2, 'x')`,
		userID, "wp3-ev-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), "DELETE FROM email_verifications WHERE user_id = $1", userID)
		_, _ = s.pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	}()

	// Plain tokens differ per mint and are never stored in the clear.
	plain1, err := s.CreateEmailVerification(userID)
	if err != nil {
		t.Fatalf("CreateEmailVerification: %v", err)
	}
	plain2, err := s.CreateEmailVerification(userID)
	if err != nil {
		t.Fatalf("CreateEmailVerification(2): %v", err)
	}
	if plain1 == plain2 || plain1 == "" {
		t.Fatalf("expected two distinct non-empty plain tokens")
	}
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_verifications WHERE token_hash = $1 OR token_hash = $2`,
		plain1, plain2).Scan(&n); err != nil {
		t.Fatalf("probe plaintext storage: %v", err)
	}
	if n != 0 {
		t.Fatalf("plain token found in storage — hash-only invariant broken")
	}

	// Unknown tokens never consume.
	if _, err := s.ConsumeEmailVerification("no-such-token"); !errors.Is(err, ErrEmailVerificationUnknown) {
		t.Fatalf("unknown token: got %v, want ErrEmailVerificationUnknown", err)
	}

	// First consume wins and returns the owning user.
	got, err := s.ConsumeEmailVerification(plain1)
	if err != nil || got != userID {
		t.Fatalf("first consume: got user=%q err=%v", got, err)
	}
	// Replay is refused even though the row still exists.
	if _, err := s.ConsumeEmailVerification(plain1); !errors.Is(err, ErrEmailVerificationUsed) {
		t.Fatalf("replay: got %v, want ErrEmailVerificationUsed", err)
	}
	// The sibling token is independent and still consumable.
	if got, err = s.ConsumeEmailVerification(plain2); err != nil || got != userID {
		t.Fatalf("sibling consume: got user=%q err=%v", got, err)
	}

	// Expired tokens (inserted with a past expires_at) are refused.
	raw := uuid.NewString()
	sum := sha256.Sum256([]byte(raw))
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO email_verifications (user_id, token_hash, expires_at)
		 VALUES ($1, $2, now() - interval '1 minute')`, userID, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}
	if _, err := s.ConsumeEmailVerification(raw); !errors.Is(err, ErrEmailVerificationExpired) {
		t.Fatalf("expired token: got %v, want ErrEmailVerificationExpired", err)
	}

	// The verified flag flips and is observable.
	if err := s.MarkUserEmailVerified(userID); err != nil {
		t.Fatalf("MarkUserEmailVerified: %v", err)
	}
	var verified bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT email_verified FROM users WHERE id = $1`, userID).Scan(&verified); err != nil {
		t.Fatalf("read email_verified: %v", err)
	}
	if !verified {
		t.Fatal("expected email_verified = true after MarkUserEmailVerified")
	}
}

// TestDeleteOrgCascade runs the destructive org path against the real schema:
// default-org refusal, running-VM refusal, then a forced delete with honest
// per-table counts and no orphaned rows.
func TestDeleteOrgCascade(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres cascade test")
	}
	s := NewStore(testDSN())
	defer s.Close()

	orgID := uuid.NewString()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO orgs (id, name, owner_id, is_default) VALUES ($1, $2, $3, false)`,
		orgID, "wp3-cascade-"+uuid.NewString()[:8], uuid.NewString()); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), "DELETE FROM orgs WHERE id = $1", orgID) }()

	projectID := uuid.NewString()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO projects (id, name, kind, bridge_subnet, org_id) VALUES ($1, $2, 'single_image', '10.77.1.0/24', $3)`,
		projectID, "wp3-proj-"+uuid.NewString()[:8], orgID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO services (id, project_id, name, image) VALUES ($1, $2, 'svc', 'img:1')`,
		uuid.NewString(), projectID); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	runningID := uuid.NewString()
	for _, r := range []struct {
		id    string
		idx   int
		state string
	}{
		{runningID, 0, "running"},
		{uuid.NewString(), 1, "stopped"},
	} {
		if _, err := s.pool.Exec(context.Background(),
			`INSERT INTO replicas (id, project_id, replica_index, state, health_status) VALUES ($1, $2, $3, $4, 'healthy')`,
			r.id, projectID, r.idx, r.state); err != nil {
			t.Fatalf("seed replica: %v", err)
		}
	}
	seeds := []struct{ label, sql string }{
		{"micro_vms", `INSERT INTO micro_vms (id, replica_id) VALUES ('mv-` + uuid.NewString()[:8] + `', '` + runningID + `')`},
		{"volumes", `INSERT INTO volumes (project_id, name, size_mib) VALUES ('` + projectID + `', 'vol', 1024)`},
		{"domains", `INSERT INTO domains (project_id, domain, kind) VALUES ('` + projectID + `', 'd` + uuid.NewString()[:8] + `.example.test', 'custom')`},
		{"environments", `INSERT INTO environments (project_id, name) VALUES ('` + projectID + `', 'prod')`},
		{"deployments", `INSERT INTO deployments (project_id) VALUES ('` + projectID + `')`},
		{"dns_records", `INSERT INTO dns_records (project_id, name, value) VALUES ('` + projectID + `', 'db', '10.0.0.7')`},
		{"secrets", `INSERT INTO secrets (project_id, name, value_encrypted) VALUES ('` + projectID + `', 'k', '\x01')`},
		{"networks", `INSERT INTO networks (project_id, name) VALUES ('` + projectID + `', 'net')`},
		{"org_members", `INSERT INTO org_members (org_id, user_id) VALUES ('` + orgID + `', '` + uuid.NewString() + `')`},
		{"usage_events", `INSERT INTO usage_events (project_id, meter, idempotency_key) VALUES ('` + projectID + `', 'vm.seconds', '` + uuid.NewString() + `')`},
	}
	for _, sd := range seeds {
		if _, err := s.pool.Exec(context.Background(), sd.sql); err != nil {
			t.Fatalf("seed %s: %v", sd.label, err)
		}
	}

	// Refusal: running VMs without force deletes nothing.
	if _, err := s.DeleteOrgCascade(orgID, false); !errors.Is(err, ErrOrgHasRunningVMs) {
		t.Fatalf("running-VM refusal: got %v, want ErrOrgHasRunningVMs", err)
	}
	var orgs int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orgs WHERE id = $1`, orgID).Scan(&orgs); err != nil || orgs != 1 {
		t.Fatalf("refused delete must keep the org (count=%d err=%v)", orgs, err)
	}

	// Forced delete succeeds with per-table counts.
	deleted, err := s.DeleteOrgCascade(orgID, true)
	if err != nil {
		t.Fatalf("forced cascade: %v", err)
	}
	want := map[string]int64{
		"micro_vms": 1, "replicas": 2, "domains": 1, "volumes": 1,
		"dns_records": 1, "environments": 1, "deployments": 1, "secrets": 1,
		"networks": 1, "services": 1, "usage_events": 1, "projects": 1,
		"org_members": 1, "orgs": 1,
	}
	for table, n := range want {
		if deleted[table] != n {
			t.Errorf("deleted[%s] = %d, want %d", table, deleted[table], n)
		}
	}

	// No orphans: every scoped row is gone alongside the org.
	for _, chk := range []struct{ table, sql string }{
		{"projects", `SELECT count(*) FROM projects WHERE id = '` + projectID + `'`},
		{"replicas", `SELECT count(*) FROM replicas WHERE project_id = '` + projectID + `'`},
		{"micro_vms", `SELECT count(*) FROM micro_vms WHERE replica_id = '` + runningID + `'`},
		{"volumes", `SELECT count(*) FROM volumes WHERE project_id = '` + projectID + `'`},
		{"secrets", `SELECT count(*) FROM secrets WHERE project_id = '` + projectID + `'`},
		{"networks", `SELECT count(*) FROM networks WHERE project_id = '` + projectID + `'`},
		{"domains", `SELECT count(*) FROM domains WHERE project_id = '` + projectID + `'`},
		{"environments", `SELECT count(*) FROM environments WHERE project_id = '` + projectID + `'`},
		{"deployments", `SELECT count(*) FROM deployments WHERE project_id = '` + projectID + `'`},
		{"dns_records", `SELECT count(*) FROM dns_records WHERE project_id = '` + projectID + `'`},
		{"usage_events", `SELECT count(*) FROM usage_events WHERE project_id = '` + projectID + `'`},
		{"org_members", `SELECT count(*) FROM org_members WHERE org_id = '` + orgID + `'`},
	} {
		var n int
		if err := s.pool.QueryRow(context.Background(), chk.sql).Scan(&n); err != nil {
			t.Fatalf("orphan check %s: %v", chk.table, err)
		}
		if n != 0 {
			t.Errorf("orphaned rows in %s after cascade: %d", chk.table, n)
		}
	}

	// Deleting an already-deleted org is an error, never a silent success.
	if _, err := s.DeleteOrgCascade(orgID, true); err == nil {
		t.Fatal("expected error deleting a missing org")
	}
}

// TestDeleteOrgCascadeRefusals pins the guards that must never delete:
// default orgs and unknown orgs.
func TestDeleteOrgCascadeRefusals(t *testing.T) {
	if testDSN() == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping Postgres cascade test")
	}
	s := NewStore(testDSN())
	defer s.Close()

	defID := uuid.NewString()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO orgs (id, name, owner_id, is_default) VALUES ($1, $2, $3, true)`,
		defID, "wp3-default-"+uuid.NewString()[:8], uuid.NewString()); err != nil {
		t.Fatalf("seed default org: %v", err)
	}
	defer func() { _, _ = s.pool.Exec(context.Background(), "DELETE FROM orgs WHERE id = $1", defID) }()

	if _, err := s.DeleteOrgCascade(defID, true); !errors.Is(err, ErrOrgIsDefault) {
		t.Fatalf("default org: got %v, want ErrOrgIsDefault (even forced)", err)
	}
	var orgs int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orgs WHERE id = $1`, defID).Scan(&orgs); err != nil || orgs != 1 {
		t.Fatalf("default org must survive (count=%d err=%v)", orgs, err)
	}

	if _, err := s.DeleteOrgCascade(uuid.NewString(), true); err == nil ||
		errors.Is(err, ErrOrgHasRunningVMs) || errors.Is(err, ErrOrgIsDefault) {
		t.Fatalf("unknown org: got %v, want a not-found error", err)
	}
}
