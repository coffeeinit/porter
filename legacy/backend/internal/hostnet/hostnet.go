// Package hostnet models host network state and change intents (FCM-17):
// read-only discovery (parse `ip -o link/addr/route`) feeds node enrollment
// and `porter doctor`; mutations are explicit intents that require an
// admin-gated, audited applier — never silent side effects from an API call.
package hostnet

import (
	"fmt"
	"net"
	"strings"
)

// Interface is one host link as discovered.
type Interface struct {
	Name   string
	Up     bool
	MTU    int
	MAC    string
	Addrs  []string // CIDRs
	Master string   // bridge master, if enslaved
}

// Route is one host route as discovered.
type Route struct {
	Dest    string
	Gateway string
	Dev     string
}

// ParseLinkLine parses one `ip -o link` line (1: lo: <LOOPBACK,UP> mtu 65536 ...).
func ParseLinkLine(line string) (Interface, error) {
	var idx int
	var name, flags string
	var mtu int
	rest := line
	if _, err := fmt.Sscanf(rest, "%d: %s %s mtu %d", &idx, &name, &flags, &mtu); err != nil {
		return Interface{}, fmt.Errorf("hostnet: bad link line %q", line)
	}
	name = strings.TrimSuffix(name, ":")
	return Interface{Name: name, Up: strings.Contains(flags, "UP"), MTU: mtu}, nil
}

// ValidateAddr gates an address assignment before any `ip addr add`.
func ValidateAddr(cidr, dev string) error {
	if dev == "" {
		return fmt.Errorf("hostnet: address change needs a device")
	}
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("hostnet: bad CIDR %q", cidr)
	}
	if ip.To4() == nil {
		return fmt.Errorf("hostnet: IPv4 only for host assignment, got %q", cidr)
	}
	return nil
}

// Change is a mutating host-network intent. Only constructed intents reach
// the privileged applier; the API layer records them as audited operations.
type Change struct {
	Op    string // up | down | addr-add | addr-del | route-add | route-del
	Dev   string
	Value string // CIDR, route dest, or empty
}

// Validate gates change intents.
func (c Change) Validate() error {
	switch c.Op {
	case "up", "down":
		if c.Dev == "" {
			return fmt.Errorf("hostnet: %s needs a device", c.Op)
		}
	case "addr-add", "addr-del":
		return ValidateAddr(c.Value, c.Dev)
	case "route-add", "route-del":
		if c.Dev == "" || c.Value == "" {
			return fmt.Errorf("hostnet: route change needs device and destination")
		}
		if _, _, err := net.ParseCIDR(c.Value); err != nil {
			if net.ParseIP(c.Value) == nil {
				return fmt.Errorf("hostnet: bad route destination %q", c.Value)
			}
		}
	default:
		return fmt.Errorf("hostnet: unknown op %q", c.Op)
	}
	return nil
}
