package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCustomDomainRequiresTXTProofBeforeRouteActivation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	project, err := s.CreateProject(ctx, "domain-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := s.CreateApp(ctx, project.ID, "domain-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	domain, err := s.CreateDomain(ctx, project.ID, app.ID, "Example.COM.", "production")
	if err != nil {
		t.Fatalf("create custom domain: %v", err)
	}
	if domain.Hostname != "example.com" || domain.Status != "pending" || domain.VerificationToken == "" || domain.VerificationRecord != "_porter-verify.example.com" {
		t.Fatalf("unexpected pending domain response: %+v", domain)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO routes(id,host,app_id,path_prefix,created_at) VALUES(?,?,?,?,?)`, NewID("rt"), domain.Hostname, app.ID, "/", domain.CreatedAt); err != nil {
		t.Fatalf("insert stale route fixture: %v", err)
	}
	if _, ok := s.RouteForHost(ctx, "EXAMPLE.COM."); ok {
		t.Fatal("stale route for a pending custom domain was served before TXT proof")
	}

	lookupName := ""
	wrongLookup := func(_ context.Context, name string) ([]string, error) {
		lookupName = name
		return []string{"wrong-token"}, nil
	}
	if err := s.verifyDomainWithLookup(ctx, domain.ID, wrongLookup); !errors.Is(err, ErrDomainProofMissing) {
		t.Fatalf("wrong TXT value error = %v, want ErrDomainProofMissing", err)
	}
	if lookupName != domain.VerificationRecord {
		t.Fatalf("TXT lookup name = %q, want %q", lookupName, domain.VerificationRecord)
	}
	if _, ok := s.RouteForHost(ctx, domain.Hostname); ok {
		t.Fatal("domain route appeared after an invalid TXT proof")
	}
	if _, err := s.CreateRoute(ctx, domain.Hostname, app.ID, "/admin"); err == nil {
		t.Fatal("manual route bypassed pending-domain TXT verification")
	}

	matchingLookup := func(_ context.Context, name string) ([]string, error) {
		lookupName = name
		return []string{"unrelated", domain.VerificationToken}, nil
	}
	if err := s.verifyDomainWithLookup(ctx, domain.ID, matchingLookup); err != nil {
		t.Fatalf("verify matching TXT proof: %v", err)
	}
	route, ok := s.RouteForHost(ctx, "example.com")
	if !ok || route.AppID != app.ID {
		t.Fatalf("verified domain did not create its route: route=%+v ok=%v", route, ok)
	}
	listed, err := s.ListDomains(ctx, project.ID)
	if err != nil || len(listed) != 1 || listed[0].Status != "active" || listed[0].VerificationToken != "" {
		t.Fatalf("verified domain metadata: domains=%+v err=%v", listed, err)
	}
	if err := s.verifyDomainWithLookup(ctx, domain.ID, func(context.Context, string) ([]string, error) {
		t.Fatal("active domain should not resolve DNS again")
		return nil, nil
	}); err != nil {
		t.Fatalf("repeat verification should be idempotent: %v", err)
	}
}

func TestManagedDomainIsActiveAndRoutedWithoutTXTProof(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	project, err := s.CreateProject(ctx, "managed-project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := s.CreateApp(ctx, project.ID, "managed-app", "image:latest", 8080, 1, 512, 1)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	domain, err := s.CreateManagedDomain(ctx, project.ID, app.ID, "Preview.Example.COM.", "preview")
	if err != nil {
		t.Fatalf("create managed domain: %v", err)
	}
	if domain.Status != "active" || domain.VerificationToken != "" {
		t.Fatalf("managed domain should be active without a TXT challenge: %+v", domain)
	}
	route, ok := s.RouteForHost(ctx, "preview.example.com")
	if !ok || route.AppID != app.ID {
		t.Fatalf("managed domain route missing: route=%+v ok=%v", route, ok)
	}
}

func TestDomainMigrationAddsVerificationTokenColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE domains(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, app_id TEXT NOT NULL, hostname TEXT NOT NULL UNIQUE, kind TEXT NOT NULL DEFAULT 'production', status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatalf("create legacy domains table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy DB: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate legacy DB: %v", err)
	}
	defer s.Close()
	var token string
	if err := s.db.QueryRow(`SELECT verification_token FROM domains LIMIT 1`).Scan(&token); !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			t.Fatalf("verification_token column missing after migration: %v", err)
		}
		t.Fatalf("unexpected legacy domain row with token %q", token)
	}
}
