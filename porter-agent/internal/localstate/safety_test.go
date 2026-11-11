package localstate

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestEnsurePrivateDirRefusesBroadExistingDirectoryWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsurePrivateDir(path); err == nil || !strings.Contains(err.Error(), "mode 0700") {
		t.Fatalf("EnsurePrivateDir error = %v, want unsafe-mode refusal", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("unsafe existing directory mode was changed to %04o", info.Mode().Perm())
	}
}

func TestEnsurePrivateDirRefusesForeignOwnerWithoutChangingIt(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing ownership requires root")
	}
	path := filepath.Join(t.TempDir(), "foreign")
	if err := os.Mkdir(path, DirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Skipf("cannot create foreign-owned directory: %v", err)
	}
	if _, err := EnsurePrivateDir(path); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("EnsurePrivateDir error = %v, want ownership refusal", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 65534 || stat.Gid != 65534 {
		t.Fatalf("foreign directory owner was changed to %d:%d", stat.Uid, stat.Gid)
	}
}

func TestAcquireJoinLockRejectsConcurrentJoin(t *testing.T) {
	dir, err := EnsurePrivateDir(filepath.Join(t.TempDir(), "agent"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := AcquireJoinLock(dir)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if second, err := AcquireJoinLock(dir); err == nil {
		_ = second.Close()
		t.Fatal("concurrent enrollment acquired the same data directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireJoinLock(dir)
	if err != nil {
		t.Fatalf("reacquire released lock: %v", err)
	}
	_ = third.Close()
}

func TestCreatePendingServiceConfigNeverReplacesExistingFile(t *testing.T) {
	dir, err := EnsurePrivateDir(filepath.Join(t.TempDir(), "agent"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-service.json")
	config := ServiceConfig{State: "pending", Server: "https://control.example", CredentialPath: CredentialPath(dir), StateDir: dir}
	if err := CreatePendingServiceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreatePendingServiceConfig(path, config); err == nil {
		t.Fatal("second pending config creation replaced the existing file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("pending config changed after rejected replacement")
	}
}
