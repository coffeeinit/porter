package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"porter/protocol"
)

type NetworkConfig struct {
	Bridge    string
	CIDR      string
	GatewayIP string
	OutIface  string
	EnableNAT bool
}

func (n NetworkConfig) prefix() string {
	if i := strings.LastIndex(n.CIDR, "/"); i >= 0 {
		return n.CIDR[i+1:]
	}
	return "16"
}

func (e *Engine) ensureNetwork(ctx context.Context, spec *protocol.VMApplySpec) (string, error) {
	if e.Network.Bridge == "" {
		return "", nil
	}
	if e.Network.GatewayIP == "" {
		return "", fmt.Errorf("network gateway ip required")
	}
	if err := run(ctx, "ip", "link", "show", e.Network.Bridge); err != nil {
		if err := run(ctx, "ip", "link", "add", "name", e.Network.Bridge, "type", "bridge"); err != nil {
			return "", err
		}
	}
	_ = run(ctx, "ip", "addr", "add", e.Network.GatewayIP+"/"+e.Network.prefix(), "dev", e.Network.Bridge)
	_ = run(ctx, "ip", "link", "set", e.Network.Bridge, "up")
	if e.Network.EnableNAT {
		_ = run(ctx, "sh", "-c", "sysctl -w net.ipv4.ip_forward=1 >/dev/null")
		out := e.Network.OutIface
		if out == "" {
			out = "eth0"
		}
		ensureRule(ctx, "iptables", "-t", "nat", "-C", "POSTROUTING", "-s", e.Network.CIDR, "-o", out, "-j", "MASQUERADE",
			"-t", "nat", "-A", "POSTROUTING", "-s", e.Network.CIDR, "-o", out, "-j", "MASQUERADE")
		ensureRule(ctx, "iptables", "-C", "FORWARD", "-i", e.Network.Bridge, "-j", "ACCEPT",
			"-A", "FORWARD", "-i", e.Network.Bridge, "-j", "ACCEPT")
		ensureRule(ctx, "iptables", "-C", "FORWARD", "-o", e.Network.Bridge, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT",
			"-A", "FORWARD", "-o", e.Network.Bridge, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	tap := spec.TapName
	if tap == "" {
		tap = tapName(spec.VMID)
	}
	_ = run(ctx, "ip", "link", "del", tap)
	if err := run(ctx, "ip", "tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
		return "", err
	}
	if err := run(ctx, "ip", "link", "set", tap, "master", e.Network.Bridge); err != nil {
		_ = run(ctx, "ip", "link", "del", tap)
		return "", err
	}
	if err := run(ctx, "ip", "link", "set", tap, "up"); err != nil {
		_ = run(ctx, "ip", "link", "del", tap)
		return "", err
	}
	return tap, nil
}

func (e *Engine) deleteTap(ctx context.Context, tap string) {
	if tap != "" {
		_ = run(ctx, "ip", "link", "del", tap)
	}
}

func tapName(vmID string) string {
	s := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(vmID))
	if len(s) > 12 {
		s = s[:12]
	}
	if len(s) < 4 {
		s = "vm" + s
	}
	return "pt" + s
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureRule(ctx context.Context, bin string, checkArgs ...string) {
	for i := 0; i < len(checkArgs); i++ {
		if checkArgs[i] == "-A" {
			if err := run(ctx, bin, checkArgs[:i]...); err == nil {
				return
			}
			_ = run(ctx, bin, checkArgs[i:]...)
			return
		}
	}
}
