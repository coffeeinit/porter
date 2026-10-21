// Package cache provides an optional, dependency-free Redis read-through
// cache for the Porter control plane. It speaks the Redis RESP protocol
// directly (a tiny, stable wire format) so `porter` keeps its single-binary,
// zero-new-module build.
//
// The cache is strictly optional. When no Redis URL is configured the
// callers fall back to [Noop], and every call behaves exactly as before —
// no connections, no goroutines, no errors, no extra latency.
package cache

import (
	"context"
	"time"
)

// Cache is the persistence surface the rest of Porter needs: a simple
// byte-string key/value store with TTL-based expiry.
//
//   - [Redis]   speaks real commands against a Redis server.
//   - [Noop]    accepts every call and never does anything — the default.
//
// Implementations must be safe for concurrent use. Values are opaque bytes;
// callers are responsible for serialization (the store uses JSON).
type Cache interface {
	// Get returns the value for key, or (nil, false) on a miss.
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set stores value under key for ttl. ttl <= 0 means "no expiry".
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Del removes one or more keys. Missing keys are not an error.
	Del(ctx context.Context, keys ...string) error
	// Flush removes every key in the selected Redis database.
	Flush(ctx context.Context) error
	// Close releases the connection pool (idempotent).
	Close() error
}

// Noop is a Cache that stores nothing. It is the default when caching is
// disabled, so the hot path never touches a network.
type Noop struct{}

// Get always misses.
func (Noop) Get(context.Context, string) ([]byte, bool) { return nil, false }

// Set accepts and discards the value.
func (Noop) Set(context.Context, string, []byte, time.Duration) error { return nil }

// Del accepts and discards the keys.
func (Noop) Del(context.Context, ...string) error { return nil }

// Flush is a no-op.
func (Noop) Flush(context.Context) error { return nil }

// Close is a no-op.
func (Noop) Close() error { return nil }
