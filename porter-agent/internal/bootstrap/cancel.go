package bootstrap

import (
	"fmt"
	"os"
)

// CancelBeforeSubmission removes the pending service config only when no HTTP
// enrollment request has been attempted. A later manual retry after an explicit
// not-consumed response uses a new Prepared instance, never this cancellation.
func (p *Prepared) CancelBeforeSubmission() error {
	if p == nil || p.submitted {
		return fmt.Errorf("cannot cancel a submitted enrollment attempt")
	}
	if p.configPath != "" {
		if err := os.Remove(p.configPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove pending agent service configuration: %w", err)
		}
	}
	return nil
}
