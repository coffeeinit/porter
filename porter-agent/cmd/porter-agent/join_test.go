//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestParseJoinOptionsRejectsTokenArgumentsWithoutEchoingThem(t *testing.T) {
	secret := "ptenr_do_not_put_this_in_argv"
	for _, args := range [][]string{
		{"--server", "https://control.example", "--token", secret},
		{"--server", "https://control.example", "--token=" + secret},
		{"--server", "https://control.example", "--token-file", "/tmp/token"},
		{"--server", "https://control.example", "--token-path", "/tmp/token"},
	} {
		_, err := parseJoinOptions(args)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("parseJoinOptions error = %v, want safe argv-token rejection", err)
		}
	}
}

func TestParseJoinOptionsEnablesOnlyNonSecretTokenStdinSwitch(t *testing.T) {
	options, err := parseJoinOptions([]string{"--server", "https://control.example", "--token-stdin"})
	if err != nil {
		t.Fatalf("parseJoinOptions: %v", err)
	}
	if !options.TokenStdin {
		t.Fatal("--token-stdin was not enabled")
	}
}

func TestParseJoinOptionsUsesFinalStateAndHostnameFlags(t *testing.T) {
	options, err := parseJoinOptions([]string{"--server", "https://control.example", "--hostname", "node-1", "--state-dir", "/srv/porter/state"})
	if err != nil {
		t.Fatalf("parseJoinOptions: %v", err)
	}
	if options.Hostname != "node-1" || options.StateDir != "/srv/porter/state" {
		t.Fatalf("parsed join options = %+v", options)
	}
	for _, oldFlag := range []string{"--name", "--data-dir"} {
		if _, err := parseJoinOptions([]string{"--server", "https://control.example", oldFlag, "ignored"}); err == nil {
			t.Errorf("legacy option %s was accepted", oldFlag)
		}
	}
}

func TestParseJoinOptionsUsesEnvironmentPathDefaultsAndFlagPrecedence(t *testing.T) {
	t.Setenv("PORTER_AGENT_STATE_DIR", "/etc/porter/state")
	t.Setenv("PORTER_AGENT_KERNEL", "/etc/porter/vmlinux")
	t.Setenv("PORTER_AGENT_ROOTFS", "/etc/porter/rootfs.ext4")
	options, err := parseJoinOptions([]string{"--server", "https://control.example"})
	if err != nil {
		t.Fatalf("parse environment defaults: %v", err)
	}
	if options.StateDir != "/etc/porter/state" || options.Kernel != "/etc/porter/vmlinux" || options.Rootfs != "/etc/porter/rootfs.ext4" {
		t.Fatalf("environment defaults = %+v", options)
	}
	options, err = parseJoinOptions([]string{"--server", "https://control.example", "--state-dir", "/srv/porter", "--kernel", "/srv/vmlinux", "--rootfs", "/srv/rootfs.ext4"})
	if err != nil {
		t.Fatalf("parse explicit overrides: %v", err)
	}
	if options.StateDir != "/srv/porter" || options.Kernel != "/srv/vmlinux" || options.Rootfs != "/srv/rootfs.ext4" {
		t.Fatalf("explicit flags did not override environment defaults: %+v", options)
	}
}

func TestValidateRunOptionsRejectsLegacyAndMalformedArgs(t *testing.T) {
	if err := validateRunOptions([]string{"--state-dir", "/var/lib/porter-agent", "--kernel", "/vmlinux", "--rootfs", "/rootfs.ext4", "--network", "off"}); err != nil {
		t.Fatalf("valid run flags rejected: %v", err)
	}
	for _, args := range [][]string{
		{"--data-dir", "/tmp/old"},
		{"--unknown", "value"},
		{"--kernel"},
		{"--network", "maybe"},
	} {
		if err := validateRunOptions(args); err == nil {
			t.Errorf("invalid run flags accepted: %v", args)
		}
	}
}

func TestReadProtectedStdinToken(t *testing.T) {
	const token = "ptenr_abc"
	got, err := readProtectedStdinToken(strings.NewReader(token + "\n"))
	if err != nil || got != token {
		t.Fatalf("stdin token = %q, %v", got, err)
	}
}

func TestReadProtectedStdinTokenRejectsOversizedLine(t *testing.T) {
	_, err := readProtectedStdinToken(strings.NewReader(strings.Repeat("x", 300) + "\n"))
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("oversized stdin token error = %v, want rejection", err)
	}
}

func TestHiddenTTYRestoresEchoOnInterrupt(t *testing.T) {
	master, slave, err := openTestPTY()
	if err != nil {
		t.Skipf("pseudo-terminal unavailable: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	var original syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&original))); errno != 0 {
		t.Fatalf("read initial terminal mode: %v", errno)
	}
	defer syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&original)))

	done := make(chan error, 1)
	go func() {
		_, err := readHiddenTTYToken(slave)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var current syscall.Termios
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&current))); errno != 0 {
			t.Fatalf("read hidden terminal mode: %v", errno)
		}
		if current.Lflag&syscall.ECHO == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("token prompt did not disable terminal echo")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("hidden token read error = %v, want interrupt result", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hidden token read did not return after SIGINT")
	}
	var restored syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&restored))); errno != 0 {
		t.Fatalf("read restored terminal mode: %v", errno)
	}
	if restored.Lflag&syscall.ECHO == 0 {
		t.Fatal("terminal echo remained disabled after SIGINT")
	}
}

func openTestPTY() (*os.File, *os.File, error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	unlocked := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlocked))); errno != 0 {
		_ = master.Close()
		return nil, nil, errno
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&number))); errno != 0 {
		_ = master.Close()
		return nil, nil, errno
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
