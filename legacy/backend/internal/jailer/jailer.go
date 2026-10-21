// Package jailer builds safe Firecracker Jailer command lines.
//
// Jailer is optional: Porter can run Firecracker directly in development, but
// production multi-tenant hosts should use Jailer or an equivalent supervisor.
package jailer

import (
	"fmt"
	"path/filepath"
	"regexp"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Config describes one Firecracker process launched through Jailer.
type Config struct {
	Binary        string
	VMID          string
	Firecracker   string
	SocketPath    string
	CgroupVersion string
}

// Args returns arguments for exec.Command(Config.Binary, Args()...). The
// Firecracker API is configured after startup, so no config file is passed.
func (c Config) Args() ([]string, error) {
	if c.Binary == "" {
		return nil, fmt.Errorf("jailer binary is required")
	}
	if c.Firecracker == "" {
		return nil, fmt.Errorf("firecracker binary is required")
	}
	if !safeID.MatchString(c.VMID) {
		return nil, fmt.Errorf("invalid VM id %q", c.VMID)
	}
	if c.SocketPath == "" || !filepath.IsAbs(c.SocketPath) {
		return nil, fmt.Errorf("socket path must be absolute")
	}
	if c.CgroupVersion == "" {
		c.CgroupVersion = "v2"
	}
	if c.CgroupVersion != "v1" && c.CgroupVersion != "v2" {
		return nil, fmt.Errorf("unsupported cgroup version %q", c.CgroupVersion)
	}
	return []string{
		"--id", c.VMID,
		"--exec-file", c.Firecracker,
		"--api-sock", c.SocketPath,
		"--cgroup-version", c.CgroupVersion,
		"--", "--api-sock", c.SocketPath,
	}, nil
}
