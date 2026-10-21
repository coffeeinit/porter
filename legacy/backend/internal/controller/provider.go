// Provider driver abstraction (OCM-06 remainder): the host reconciler must
// never assume a cloud API. Providers implement existence checks; the static
// driver (heartbeat-only, bare metal / customer hosts) never auto-terminates
// — silence yields NEEDS_ATTENTION operations, not destruction.
package controller

import "context"

// Provider answers infrastructure existence for reconciler decisions.
type Provider interface {
	// InstanceExists reports whether the provider still has the node.
	InstanceExists(ctx context.Context, nodeID string) (bool, error)
	// Name identifies the driver (gcp | hetzner | static …).
	Name() string
}

// StaticProvider is the heartbeat-only driver: no cloud API, so existence
// is unknown and the reconciler must not terminate. Matches the OCM
// HeartbeatOnlyChecker semantics.
type StaticProvider struct{}

// Name implements Provider.
func (StaticProvider) Name() string { return "static" }

// InstanceExists implements Provider: always unknown (false, nil) — the
// caller treats unknown as "keep, keep alerting".
func (StaticProvider) InstanceExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}
