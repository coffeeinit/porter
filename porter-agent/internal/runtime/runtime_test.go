package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestSnapshotRejectsSocketOutsideRuntimeDirectory(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	engine := New(base, "", "", NetworkConfig{})
	_, _, _, err := engine.Snapshot(context.Background(), store.VM{ID: "vm-1", Socket: filepath.Join(outside, "other.sock")}, "snap-1")
	if err == nil || !strings.Contains(err.Error(), "socket path") {
		t.Fatalf("Snapshot accepted a foreign socket path: %v", err)
	}
}

func TestHealthyRejectsSocketOutsideRuntimeDirectory(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	foreignSocket := filepath.Join(outside, "other.sock")
	listener, err := net.Listen("unix", foreignSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	engine := New(base, "", "", NetworkConfig{})
	engine.supervisor = &fakeProcessSupervisor{active: true, pid: 4242}
	if engine.Healthy(store.VM{ID: "vm-1", PID: 4242, Socket: foreignSocket}) {
		t.Fatal("Healthy accepted a foreign Unix socket")
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

type fakeProcessSupervisor struct {
	mu       sync.Mutex
	active   bool
	pid      int
	starts   int
	server   *http.Server
	listener net.Listener
}

func (s *fakeProcessSupervisor) Start(_ context.Context, spec processSpec) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return s.pid, nil
	}
	listener, err := net.Listen("unix", spec.Socket)
	if err != nil {
		return 0, err
	}
	s.listener = listener
	s.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	s.active = true
	s.starts++
	if s.pid == 0 {
		s.pid = 4242
	}
	go func(server *http.Server, ln net.Listener) { _ = server.Serve(ln) }(s.server, listener)
	return s.pid, nil
}

func (s *fakeProcessSupervisor) Status(_ context.Context, _ string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid, s.active, nil
}

func (s *fakeProcessSupervisor) Stop(_ context.Context, _ string) error {
	s.mu.Lock()
	server := s.server
	s.active = false
	s.mu.Unlock()
	if server != nil {
		return server.Close()
	}
	return nil
}

func (s *fakeProcessSupervisor) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func TestVMIdentityAndControlPlaneTapNames(t *testing.T) {
	vmID := "vm_0123456789abcdef"
	if !validVMID(vmID) {
		t.Fatalf("control-plane VM ID %q was rejected", vmID)
	}
	if validVMID("_invalid") || validVMID("../bad") {
		t.Fatal("unsafe VM identity was accepted")
	}
	for _, tap := range []string{controlPlaneTapName(vmID), tapName(vmID), legacyControlPlaneTapName(vmID)} {
		if !validTapNameForVM(vmID, tap) {
			t.Errorf("deterministic TAP name %q was rejected for %q", tap, vmID)
		}
	}
	if validTapNameForVM(vmID, "eth0") {
		t.Fatal("unrelated TAP name was accepted")
	}
	first, second := "vm_1234567890abcdef", "vm_abcdef7890abcdef"
	if legacyControlPlaneTapName(first) != legacyControlPlaneTapName(second) {
		t.Fatal("test VM IDs do not reproduce the legacy suffix collision")
	}
	if tapName(first) == tapName(second) || len(tapName(first)) > 15 {
		t.Fatalf("hashed TAP names collide or exceed Linux's 15-character limit: %q / %q", tapName(first), tapName(second))
	}
}

func TestVMDirRejectsSymlinkedVMDirectory(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "vm-1")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	engine := New(base, "", "", NetworkConfig{})
	if _, err := engine.vmDir("vm-1", true); err == nil {
		t.Fatal("symlinked VM directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime state was written outside the base directory: %v", err)
	}
}

func TestStopRejectsSocketOutsideRuntimeDirectory(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	foreign := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(foreign, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := &fakeProcessSupervisor{}
	engine := New(base, "", "", NetworkConfig{})
	engine.supervisor = supervisor
	err := engine.Stop(store.VM{ID: "vm-1", Socket: foreign})
	if err == nil || !strings.Contains(err.Error(), "socket path") {
		t.Fatalf("Stop socket validation error = %v", err)
	}
	if contents, err := os.ReadFile(foreign); err != nil || string(contents) != "do not remove" {
		t.Fatalf("foreign file changed: contents=%q err=%v", contents, err)
	}
}

func TestDeleteTapRefusesUnownedInterface(t *testing.T) {
	binDir, marker := t.TempDir(), filepath.Join(t.TempDir(), "deleted")
	script := "#!/bin/sh\ncase \"$*\" in\n  '-j -d link show dev ptvm1') printf '%s\\n' '[{\"ifname\":\"ptvm1\",\"ifalias\":\"someone-else\"}]' ;;\n  'link del ptvm1') touch \"" + marker + "\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ip"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	engine := New(t.TempDir(), "", "", NetworkConfig{})
	if err := engine.deleteTap(context.Background(), "vm-1", "ptvm1"); err == nil {
		t.Fatal("unowned TAP interface was accepted for deletion")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unowned TAP was deleted: %v", err)
	}
}

func TestEnsureNetworkRefusesUnownedExistingTap(t *testing.T) {
	binDir, marker := t.TempDir(), filepath.Join(t.TempDir(), "deleted")
	script := "#!/bin/sh\ncase \"$*\" in\n  'link show porter0') exit 0 ;;\n  '-j -d link show dev ptvm1') printf '%s\\n' '[{\"ifname\":\"ptvm1\",\"ifalias\":\"foreign-owner\"}]' ;;\n  'link del ptvm1'|'tuntap add dev ptvm1 mode tap') touch \"" + marker + "\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ip"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	engine := New(t.TempDir(), "", "", NetworkConfig{Bridge: "porter0", CIDR: "10.0.0.0/24", GatewayIP: "10.0.0.1"})
	spec := protocol.VMApplySpec{VMID: "vm-1", TapName: "ptvm1"}
	if _, err := engine.ensureNetwork(context.Background(), &spec); err == nil {
		t.Fatal("network setup accepted a TAP with a foreign ownership alias")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("network setup deleted or recreated an unowned TAP: %v", err)
	}
}

func TestStopReportsTapCleanupFailure(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  '-j -d link show dev ptvm1') printf '%s\\n' '[{\"ifname\":\"ptvm1\",\"ifalias\":\"porter-vm:vm-1\"}]' ;;\n  'link del ptvm1') echo simulated-delete-failure >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ip"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	engine := New(t.TempDir(), "", "", NetworkConfig{})
	engine.supervisor = &fakeProcessSupervisor{}
	err := engine.Stop(store.VM{ID: "vm-1", TapName: "ptvm1"})
	if err == nil || !strings.Contains(err.Error(), "delete TAP") {
		t.Fatalf("Stop should report TAP teardown failure, got %v", err)
	}
}

func TestApplyIsIdempotentAndRestartableWithSupervisor(t *testing.T) {
	base := t.TempDir()
	kernel, rootfs := filepath.Join(t.TempDir(), "kernel"), filepath.Join(t.TempDir(), "rootfs")
	for _, path := range []string{kernel, rootfs} {
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine := New(base, kernel, rootfs, NetworkConfig{})
	supervisor := &fakeProcessSupervisor{}
	engine.supervisor = supervisor
	spec := protocol.VMApplySpec{VMID: "vm_0123456789abcdef", VCPUs: 1, MemoryMB: 256}

	first, err := engine.Apply(context.Background(), spec)
	if err != nil || first.State != "running" {
		t.Fatalf("first apply = %+v, err=%v", first, err)
	}
	second, err := engine.Apply(context.Background(), spec)
	if err != nil || second.State != "running" {
		t.Fatalf("idempotent apply = %+v, err=%v", second, err)
	}
	if got := supervisor.startCount(); got != 1 {
		t.Fatalf("idempotent apply launched %d services, want 1", got)
	}
	restarted, err := engine.Restart(context.Background(), spec)
	if err != nil || restarted.State != "running" {
		t.Fatalf("restart = %+v, err=%v", restarted, err)
	}
	if got := supervisor.startCount(); got != 2 {
		t.Fatalf("restart launched %d total services, want 2", got)
	}
	if err := engine.Stop(restarted); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := engine.Delete(context.Background(), restarted); err != nil {
		t.Fatalf("delete stopped VM: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, restarted.ID, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime manifest remains after delete: %v", err)
	}
}
