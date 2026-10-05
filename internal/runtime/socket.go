package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// socketPath returns the only supported Firecracker control-socket location
// for a VM. IDs are sanitized so a project or replica identifier cannot escape
// the configured socket directory.
func socketPath(socketDir, vmID string) string {
	return filepath.Join(socketDir, "porter-"+safeVMID(vmID)+".sock")
}

func safeVMID(vmID string) string {
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(vmID)
}

// waitForSocket waits until Firecracker accepts a Unix-socket connection or
// the boot context expires. It deliberately never falls back to TCP.
func waitForSocket(ctx context.Context, path string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}

// removeSocket removes only a socket owned by the runtime. Missing sockets are
// already-clean state and therefore are not reported as lifecycle failures.
func removeSocket(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
