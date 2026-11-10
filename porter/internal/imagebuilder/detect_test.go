package imagebuilder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectReact(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "package.json"), []byte(`{"dependencies":{"react":"18"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFramework(d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Framework != FrameworkReact {
		t.Fatalf("framework=%s want react", got.Framework)
	}
}

func TestParseCompose(t *testing.T) {
	d := t.TempDir()
	body := "services:\n  web:\n    build: .\n    ports:\n      - \"3000:3000\"\n"
	p := filepath.Join(d, "docker-compose.yml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	svc, err := ParseCompose(p)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "web" {
		t.Fatalf("service=%s", svc.Name)
	}
	if svc.ContextDir != d {
		t.Fatalf("context=%s want %s", svc.ContextDir, d)
	}
}

func TestIsGitCommitSHA(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{ref: "0123456789abcdef0123456789abcdef01234567", want: true},
		{ref: "ABCDEF0123456789ABCDEF0123456789ABCDEF01", want: true},
		{ref: "main", want: false},
		{ref: "0123456789abcdef0123456789abcdef0123456g", want: false},
		{ref: "0123456789abcdef0123456789abcdef0123456", want: false},
		{ref: "0123456789abcdef0123456789abcdef012345670", want: false},
	} {
		if got := isGitCommitSHA(tc.ref); got != tc.want {
			t.Errorf("isGitCommitSHA(%q)=%v want %v", tc.ref, got, tc.want)
		}
	}
}

func TestCheckoutGitSourcePinsRequestedCommit(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "config", "user.email", "porter-test@example.invalid"}, {"-C", repo, "config", "user.name", "Porter test"}} {
		if out, err := execCommand(ctx, "git", args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	file := filepath.Join(repo, "version.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", repo, "add", "version.txt"}, {"-C", repo, "commit", "-m", "first"}} {
		if out, err := execCommand(ctx, "git", args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	first, err := execCommand(ctx, "git", "-C", repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", repo, "add", "version.txt"}, {"-C", repo, "commit", "-m", "second"}} {
		if out, err := execCommand(ctx, "git", args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if err := checkoutGitSource(ctx, repo, first, clone); err != nil {
		t.Fatalf("checkout pinned commit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(clone, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\n" {
		t.Fatalf("checkout contents=%q want first commit", got)
	}
}
