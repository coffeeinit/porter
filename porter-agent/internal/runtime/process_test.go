package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdPropertiesValidateLimitsAndEscapePaths(t *testing.T) {
	spec := processSpec{
		VMID:       "vm_0123456789abcdef",
		WorkDir:    "/tmp/porter work;not-a-shell-command",
		LogPath:    "/tmp/porter logs/firecracker.log",
		KernelPath: "/tmp/kernel image",
		RootfsPath: "/tmp/root image",
		Socket:     "/tmp/porter work/firecracker.sock",
		VCPUs:      2,
		MemoryMB:   256,
	}
	properties, err := systemdProperties(spec)
	if err != nil {
		t.Fatalf("systemdProperties: %v", err)
	}
	joined := strings.Join(properties, "\n")
	if !strings.Contains(joined, "WorkingDirectory=/tmp/porter\\swork;not-a-shell-command") {
		t.Fatalf("working directory was not encoded as a single systemd property: %q", joined)
	}
	if !strings.Contains(joined, "MemoryMax=") || !strings.Contains(joined, "CPUQuota=200%") {
		t.Fatalf("resource limits missing: %q", joined)
	}
	for _, invalid := range []processSpec{
		{VMID: "../bad", WorkDir: "/tmp", LogPath: "/tmp/log", KernelPath: "/tmp/kernel", RootfsPath: "/tmp/root", Socket: "/tmp/socket", VCPUs: 1, MemoryMB: 256},
		{VMID: "vm-1", WorkDir: "/tmp/line\nbreak", LogPath: "/tmp/log", KernelPath: "/tmp/kernel", RootfsPath: "/tmp/root", Socket: "/tmp/socket", VCPUs: 1, MemoryMB: 256},
		{VMID: "vm-1", WorkDir: "/tmp", LogPath: "/tmp/log", KernelPath: "/tmp/kernel", RootfsPath: "/tmp/root", Socket: "/tmp/socket", VCPUs: 1, MemoryMB: 1<<20 + 1},
	} {
		if _, err := systemdProperties(invalid); err == nil {
			t.Errorf("invalid process spec was accepted: %+v", invalid)
		}
	}
}

func TestSystemdSupervisorStartStatusAndStopWithCommandShims(t *testing.T) {
	temp := t.TempDir()
	statePath := filepath.Join(temp, "unit-active")
	argsPath := filepath.Join(temp, "systemd-run-args")
	workDir := filepath.Join(temp, "work dir;still-data")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernel := filepath.Join(temp, "kernel image")
	rootfs := filepath.Join(temp, "root image")
	firecracker := filepath.Join(temp, "fake firecracker")
	for _, path := range []string{kernel, rootfs} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, firecracker, "#!/bin/sh\nexit 0\n")
	runPath := filepath.Join(temp, "systemd-run")
	writeExecutable(t, runPath, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\ntouch \"$STATE_PATH\"\n")
	ctlPath := filepath.Join(temp, "systemctl")
	writeExecutable(t, ctlPath, "#!/bin/sh\ncase \"$1\" in\n  reset-failed) exit 0 ;;\n  show)\n    if [ -f \"$STATE_PATH\" ]; then printf 'LoadState=loaded\\nActiveState=active\\nMainPID=4242\\n'; else printf 'LoadState=not-found\\nActiveState=inactive\\nMainPID=0\\n'; fi ;;\n  stop|kill) rm -f \"$STATE_PATH\" ;;\n  *) exit 0 ;;\nesac\n")
	t.Setenv("ARGS_PATH", argsPath)
	t.Setenv("STATE_PATH", statePath)
	supervisor := &systemdSupervisor{SystemdRun: runPath, Systemctl: ctlPath, PollEvery: 5 * time.Millisecond, StartLimit: time.Second, StopLimit: time.Second}
	spec := processSpec{
		VMID:       "vm_0123456789abcdef",
		Binary:     firecracker,
		Socket:     filepath.Join(workDir, "firecracker.sock"),
		WorkDir:    workDir,
		LogPath:    filepath.Join(workDir, "firecracker.log"),
		KernelPath: kernel,
		RootfsPath: rootfs,
		VCPUs:      2,
		MemoryMB:   256,
	}
	pid, err := supervisor.Start(context.Background(), spec)
	if err != nil || pid != 4242 {
		t.Fatalf("Start = pid %d, err %v", pid, err)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsData)
	if !strings.Contains(args, "--unit=porter-vm-vm_0123456789abcdef") || !strings.Contains(args, "WorkingDirectory="+strings.ReplaceAll(workDir, " ", `\s`)) {
		t.Fatalf("systemd-run arguments missing unit or escaped path: %q", args)
	}
	if _, active, err := supervisor.Status(context.Background(), spec.VMID); err != nil || !active {
		t.Fatalf("Status after start = active %v, err %v", active, err)
	}
	if err := supervisor.Stop(context.Background(), spec.VMID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, active, err := supervisor.Status(context.Background(), spec.VMID); err != nil || active {
		t.Fatalf("Status after stop = active %v, err %v", active, err)
	}
}
