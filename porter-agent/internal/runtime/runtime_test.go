package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"porter/agent/internal/store"
	"porter/protocol"
)

func TestCommandUsesAPISocket(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmdline []byte
		socket  string
		want    bool
	}{
		{name: "matching socket", cmdline: []byte("firecracker\x00--api-sock\x00/run/porter/vm-1/firecracker.sock\x00"), socket: "/run/porter/vm-1/firecracker.sock", want: true},
		{name: "different socket", cmdline: []byte("firecracker\x00--api-sock\x00/run/porter/vm-2/firecracker.sock\x00"), socket: "/run/porter/vm-1/firecracker.sock", want: false},
		{name: "no API socket argument", cmdline: []byte("unrelated-process\x00"), socket: "/run/porter/vm-1/firecracker.sock", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandUsesAPISocket(tc.cmdline, tc.socket); got != tc.want {
				t.Fatalf("commandUsesAPISocket() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCommandMatchesFirecrackerIdentity(t *testing.T) {
	cmdline := []byte("firecracker\x00--api-sock\x00/run/porter/vm-1/firecracker.sock\x00")
	if !commandMatchesFirecracker(cmdline, "/usr/bin/firecracker", "/run/porter/vm-1/firecracker.sock") {
		t.Fatal("matching Firecracker command was rejected")
	}
	if commandMatchesFirecracker(cmdline, "other-vmm", "/run/porter/vm-1/firecracker.sock") {
		t.Fatal("unrelated VMM with a matching socket argument was accepted")
	}
}

func TestSnapshotRejectsUnsafeIDsBeforeTouchingFilesystem(t *testing.T) {
	engine := New(t.TempDir(), "", "", NetworkConfig{})
	for _, id := range []string{"", ".", "..", "../outside", "vm/../../etc", "name.with.dot"} {
		t.Run(id, func(t *testing.T) {
			_, _, _, err := engine.Snapshot(context.Background(), store.VM{ID: "vm-1", Socket: "/unused.sock"}, id)
			if err == nil || !strings.Contains(err.Error(), "invalid snapshot id") {
				t.Fatalf("snapshot id %q error = %v", id, err)
			}
		})
	}
}

func TestValidateSnapshotArtifactConfinesRegularFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vm-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "snap-1.snap")
	if err := os.WriteFile(state, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotArtifact(root, state, ".snap"); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.snap")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotArtifact(root, outside, ".snap"); err == nil {
		t.Fatal("snapshot outside VM directory was accepted")
	}
	link := filepath.Join(root, "link.snap")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateSnapshotArtifact(root, link, ".snap"); err == nil {
		t.Fatal("snapshot symlink was accepted")
	}
}

func TestValidateSnapshotArtifactRejectsSymlinkedVMDirectory(t *testing.T) {
	outside := t.TempDir()
	linkParent := t.TempDir()
	root := filepath.Join(linkParent, "vm-1")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path := filepath.Join(root, "snap-1.snap")
	if err := os.WriteFile(filepath.Join(outside, "snap-1.snap"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotArtifact(root, path, ".snap"); err == nil {
		t.Fatal("artifact in a symlinked VM directory was accepted")
	}
}

func TestRestoreSnapshotRejectsUnsafePathsBeforeRuntimeSideEffects(t *testing.T) {
	base := t.TempDir()
	engine := New(base, "/does/not/exist", "", NetworkConfig{})
	vm := store.VM{ID: "vm-1", PID: -1, Socket: filepath.Join(base, "vm-1", "firecracker.sock")}
	if err := os.MkdirAll(filepath.Join(base, "vm-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := engine.RestoreSnapshot(context.Background(), vm, "/etc/snap-1.snap", "/etc/snap-1.mem", strings.Repeat("0", 64), strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "outside the VM directory") {
		t.Fatalf("unsafe restore path error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "vm-1", "firecracker-restore.log")); !os.IsNotExist(err) {
		t.Fatalf("restore launched before validating paths: stat err=%v", err)
	}
}

func TestVerifySnapshotDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot")
	contents := []byte("snapshot payload")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	want := hex.EncodeToString(sum[:])
	if err := verifySnapshotDigest(path, want); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	for _, invalid := range []string{"", "not-a-digest", strings.Repeat("0", 64)} {
		if err := verifySnapshotDigest(path, invalid); err == nil {
			t.Errorf("invalid digest %q was accepted", invalid)
		}
	}
}

func TestStopWaitsForAndReapsMatchingFirecrackerProcess(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "firecracker.sock")
	ready := filepath.Join(dir, "ready")
	program := "import signal,time,sys; signal.signal(signal.SIGTERM, lambda *_: (time.sleep(0.1), sys.exit(0))); open(sys.argv[1], 'w').close(); time.sleep(60)"
	cmd := exec.Command("python3", "-c", program, ready, "--api-sock", socket)
	cmd.Args[0] = "firecracker"
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatal("helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	vm := store.VM{ID: "vm-1", PID: cmd.Process.Pid, Socket: socket}
	engine := &Engine{FirecrackerBin: "firecracker"}
	if running, err := engine.ProcessRunning(vm); err != nil || !running {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("helper process identity: running=%v err=%v", running, err)
	}
	if err := engine.Stop(vm); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("stop helper process: %v", err)
	}
	if err := syscall.Kill(vm.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process PID %d still exists after Stop: %v", vm.PID, err)
	}
}

func TestKVMFirecrackerApplyStopAcceptance(t *testing.T) {
	if os.Getenv("PORTER_KVM_ACCEPTANCE") != "1" {
		t.Skip("set PORTER_KVM_ACCEPTANCE=1 on a KVM-capable host to run the Firecracker acceptance test")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Fatalf("KVM device is unavailable: %v", err)
	}
	kernel := os.Getenv("PORTER_KVM_KERNEL")
	rootfs := os.Getenv("PORTER_KVM_ROOTFS")
	for name, path := range map[string]string{"PORTER_KVM_KERNEL": kernel, "PORTER_KVM_ROOTFS": rootfs} {
		if path == "" {
			t.Fatalf("%s must point to a tested Linux kernel/rootfs artifact", name)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must be a readable regular file: path=%q err=%v", name, path, err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		_ = f.Close()
	}
	firecracker := os.Getenv("PORTER_FIRECRACKER_BIN")
	if firecracker == "" {
		firecracker = "firecracker"
	}
	bin, err := exec.LookPath(firecracker)
	if err != nil {
		t.Fatalf("find Firecracker binary: %v", err)
	}
	engine := New(t.TempDir(), kernel, rootfs, NetworkConfig{})
	engine.FirecrackerBin = bin
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	vm, err := engine.Apply(ctx, protocol.VMApplySpec{VMID: "kvm-acceptance", VCPUs: 1, MemoryMB: 256})
	if err != nil {
		t.Fatalf("Firecracker apply: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = engine.Stop(vm)
		}
	})
	if !engine.Healthy(vm) {
		t.Fatal("Firecracker VM process is not healthy after apply")
	}
	if err := engine.Stop(vm); err != nil {
		t.Fatalf("Firecracker stop: %v", err)
	}
	stopped = true
	if running, err := engine.ProcessRunning(vm); err != nil || running {
		t.Fatalf("Firecracker process remains after stop: running=%v err=%v", running, err)
	}
}
