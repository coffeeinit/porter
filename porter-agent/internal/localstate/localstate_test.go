package localstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialWriteReadAndReplacement(t *testing.T) {
	dir, err := EnsurePrivateDir(filepath.Join(t.TempDir(), "agent"))
	if err != nil {
		t.Fatal(err)
	}
	path := CredentialPath(dir)
	if err := WriteCredential(path, "node-secret-one"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCredential(path); err != nil || got != "node-secret-one" {
		t.Fatalf("first credential read = %q, %v", got, err)
	}
	if err := WriteCredential(path, "node-secret-two"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadCredential(path); err != nil || got != "node-secret-two" {
		t.Fatalf("replaced credential read = %q, %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != PrivateFileMode {
		t.Fatalf("credential mode=%04o, want 0600", info.Mode().Perm())
	}
}

func TestCredentialReaderRejectsSymlinksAndPermissiveModes(t *testing.T) {
	dir, err := EnsurePrivateDir(filepath.Join(t.TempDir(), "agent"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside-secret")
	if err := os.WriteFile(target, []byte("node-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-credential")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredential(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink credential read error = %v, want rejection", err)
	}
	permissive := filepath.Join(dir, "permissive-credential")
	if err := os.WriteFile(permissive, []byte("node-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredential(permissive); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("permissive credential read error = %v, want mode rejection", err)
	}
}

func TestEnsurePrivateDirRejectsFilesystemRootAndSymlink(t *testing.T) {
	if _, err := EnsurePrivateDir(string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root accepted as data directory")
	}
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsurePrivateDir(link); err == nil {
		t.Fatal("symlink accepted as data directory")
	}
}

func TestReadyServiceConfigMustBeCompleteAndDoesNotContainCredential(t *testing.T) {
	dir, err := EnsurePrivateDir(filepath.Join(t.TempDir(), "agent"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-service.json")
	config := ServiceConfig{
		State: "ready", Server: "https://control.example",
		NodeID: "node-01", CredentialPath: CredentialPath(dir), CredentialGeneration: 1,
		StateDir: dir, Kernel: "/var/lib/porter-agent/vmlinux", Rootfs: "/var/lib/porter-agent/rootfs.ext4",
	}
	if err := WriteServiceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadServiceConfig(path)
	if err != nil || loaded.NodeID != config.NodeID {
		t.Fatalf("read service config: %+v, %v", loaded, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "node-secret") {
		t.Fatal("service config unexpectedly contains a node credential")
	}
	if strings.Contains(string(contents), "hostname") || strings.Contains(string(contents), "agent-01") {
		t.Fatal("service config unexpectedly persisted the enrollment hostname")
	}
	assertNoTempFiles(t, dir)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".porter-agent-private-") {
			t.Fatalf("atomic temporary file was left behind: %s", entry.Name())
		}
	}
}
