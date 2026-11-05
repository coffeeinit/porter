package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if !validTapNameForVM(spec.VMID, tap) {
		return "", fmt.Errorf("tap name does not match VM identity")
	}
	alias, exists, err := tapAlias(ctx, tap)
	if err != nil {
		return "", err
	}
	wantAlias := "porter-vm:" + spec.VMID
	if exists && alias != wantAlias {
		return "", fmt.Errorf("TAP interface %q is not owned by VM %s", tap, spec.VMID)
	}
	created := false
	if !exists {
		if err := run(ctx, "ip", "tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
			return "", err
		}
		created = true
		if err := run(ctx, "ip", "link", "set", "dev", tap, "alias", wantAlias); err != nil {
			cleanupErr := run(ctx, "ip", "link", "del", tap)
			if cleanupErr != nil {
				return "", fmt.Errorf("set TAP ownership alias: %w; remove newly created TAP: %v", err, cleanupErr)
			}
			return "", fmt.Errorf("set TAP ownership alias: %w", err)
		}
	}
	if err := run(ctx, "ip", "link", "set", tap, "master", e.Network.Bridge); err != nil {
		return "", e.cleanupCreatedTap(ctx, spec.VMID, tap, created, err)
	}
	if err := run(ctx, "ip", "link", "set", tap, "up"); err != nil {
		return "", e.cleanupCreatedTap(ctx, spec.VMID, tap, created, err)
	}
	return tap, nil
}

func (e *Engine) cleanupCreatedTap(ctx context.Context, vmID, tap string, created bool, cause error) error {
	if !created {
		return cause
	}
	if err := e.deleteTap(ctx, vmID, tap); err != nil {
		return fmt.Errorf("%w; TAP cleanup failed: %v", cause, err)
	}
	return cause
}

func (e *Engine) deleteTap(ctx context.Context, vmID, tap string) error {
	if tap == "" {
		return nil
	}
	if !validTapNameForVM(vmID, tap) {
		return fmt.Errorf("refuse to delete TAP interface %q for VM %q: name does not match", tap, vmID)
	}
	alias, exists, err := tapAlias(ctx, tap)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if alias != "porter-vm:"+vmID {
		return fmt.Errorf("refuse to delete TAP interface %q: ownership alias does not match VM %q", tap, vmID)
	}
	if err := run(ctx, "ip", "link", "del", tap); err != nil {
		return fmt.Errorf("delete TAP interface %q: %w", tap, err)
	}
	return nil
}

func tapAlias(ctx context.Context, tap string) (string, bool, error) {
	out, err := exec.CommandContext(ctx, "ip", "-j", "-d", "link", "show", "dev", tap).CombinedOutput()
	if err != nil {
		message := strings.ToLower(string(out) + " " + err.Error())
		if strings.Contains(message, "cannot find device") || strings.Contains(message, "does not exist") || strings.Contains(message, "no such device") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect TAP interface %q: %w: %s", tap, err, strings.TrimSpace(string(out)))
	}
	var links []struct {
		IfName  string `json:"ifname"`
		IfAlias string `json:"ifalias"`
	}
	if err := json.Unmarshal(out, &links); err != nil || len(links) != 1 || links[0].IfName != tap {
		return "", false, fmt.Errorf("inspect TAP interface %q: invalid ip JSON response", tap)
	}
	return links[0].IfAlias, true, nil
}

func tapName(vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	return "pt" + hex.EncodeToString(sum[:])[:13]
}

func legacyRuntimeTapName(vmID string) string {
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

func validTapNameForVM(vmID, tap string) bool {
	if !validVMID(vmID) || tap == "" || len(tap) > 15 {
		return false
	}
	return tap == tapName(vmID) || tap == legacyRuntimeTapName(vmID) || tap == legacyControlPlaneTapName(vmID)
}

func controlPlaneTapName(vmID string) string { return tapName(vmID) }

func legacyControlPlaneTapName(vmID string) string {
	suffix := vmID
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	suffix = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(suffix))
	return "pt" + suffix
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
