//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func main() {
	ip, gw, mask, dns := guestConfig()
	if ip != "" {
		_ = run("ip", "link", "set", "eth0", "up")
		_ = run("ip", "addr", "add", ip+"/"+mask, "dev", "eth0")
		if gw != "" {
			_ = run("ip", "route", "add", "default", "via", gw, "dev", "eth0")
		}
	}
	if dns != "" {
		_ = os.WriteFile("/etc/resolv.conf", []byte("nameserver "+dns+"\n"), 0644)
	}
	entry := "/sbin/init"
	for _, p := range []string{"/porter/entrypoint", "/app/entrypoint", "/sbin/init"} {
		if _, err := os.Stat(p); err == nil {
			entry = p
			break
		}
	}
	argv := []string{entry}
	if strings.HasSuffix(entry, "entrypoint") {
		argv = append(argv, os.Args[1:]...)
	}
	_ = syscall.Exec(entry, argv, os.Environ())
}

func guestConfig() (ip, gw, mask, dns string) {
	b, _ := os.ReadFile("/proc/cmdline")
	for _, f := range strings.Fields(string(b)) {
		switch {
		case strings.HasPrefix(f, "porter.ip="):
			ip = strings.TrimPrefix(f, "porter.ip=")
		case strings.HasPrefix(f, "porter.gw="):
			gw = strings.TrimPrefix(f, "porter.gw=")
		case strings.HasPrefix(f, "porter.mask="):
			mask = strings.TrimPrefix(f, "porter.mask=")
		case strings.HasPrefix(f, "porter.dns="):
			dns = strings.TrimPrefix(f, "porter.dns=")
		}
	}
	if mask == "" {
		mask = "16"
	}
	return
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v: %s\n", name, err, strings.TrimSpace(string(out)))
	}
	return err
}
