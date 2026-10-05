// Package metadata implements the per-VM metadata identity pattern (OCM-18):
// the control plane mints a nonce, passes it to the guest via the kernel
// command line (metadata_nonce=...), and the guest presents it back in the
// X-Metadata-Nonce header. Comparison is constant-time; secrets pulled
// through this channel are cached for SecretCacheTTL (60s).
package metadata

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// SecretCacheTTL bounds how long per-VM secrets stay in agent memory.
const SecretCacheTTL = 60 * time.Second

// NonceLen is the raw byte length of a metadata nonce (hex-encoded on the wire).
const NonceLen = 16

// Mint returns a fresh random nonce for one VM boot. A new nonce is minted
// per boot so a leaked value dies with the VM.
func Mint() (string, error) {
	b := make([]byte, NonceLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("metadata: mint nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// KernelArg renders the kernel command-line fragment carrying the nonce,
// e.g. "metadata_nonce=abc123". The guest init script reads /proc/cmdline.
func KernelArg(nonce string) string {
	return "metadata_nonce=" + nonce
}

// Verify compares the presented header value against the expected nonce in
// constant time. Empty values always fail (empty-token-fatal).
func Verify(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// Cache is a TTL-bounded per-VM secret cache for the agent side.
type Cache struct {
	mu    sync.Mutex
	items map[string]entry
	now   func() time.Time
	ttl   time.Duration
}

type entry struct {
	value     map[string]string
	expiresAt time.Time
}

// NewCache builds a cache with the standard TTL (override ttl with <=0).
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = SecretCacheTTL
	}
	return &Cache{items: map[string]entry{}, now: time.Now, ttl: ttl}
}

// Put stores secrets for vmID, replacing any previous entry.
func (c *Cache) Put(vmID string, secrets map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string]string, len(secrets))
	for k, v := range secrets {
		cp[k] = v
	}
	c.items[vmID] = entry{value: cp, expiresAt: c.now().Add(c.ttl)}
}

// Get returns secrets for vmID, or false on miss/expiry. Expired entries
// are evicted on read.
func (c *Cache) Get(vmID string) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[vmID]
	if !ok || !c.now().Before(e.expiresAt) {
		delete(c.items, vmID)
		return nil, false
	}
	return e.value, true
}

// Invalidate drops vmID immediately (call on VM stop).
func (c *Cache) Invalidate(vmID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, vmID)
}

// NonceFromCmdline extracts metadata_nonce from a /proc/cmdline string.
// It reports false when absent so callers fail closed.
func NonceFromCmdline(cmdline string) (string, bool) {
	for _, f := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(f, "metadata_nonce="); ok && v != "" {
			return v, true
		}
	}
	return "", false
}
