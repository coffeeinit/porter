// Quick-share via cloudflared (localhost-share): expose one local address
// on a public trycloudflare.com URL for previews, webhooks, and demos.
// This is the "sometimes" tunnel — ephemeral by design, no DNS, no account.
// Limits (upstream): ~200 concurrent requests, NO server-sent events over
// quick tunnels — use a named tunnel for SSE/streaming workloads.
// The primary edge stays DNS + orange-cloud proxy (see cloudflare.go).
// Execution needs the cloudflared binary on the host; without it every op
// fails explicitly. Shares are tracked in a registry so leaks are visible
// and reaped.
package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Share is one live quick tunnel.
type Share struct {
	ID     string // porter-side id
	URL    string // public https://*.trycloudflare.com URL
	Local  string // local address shared, e.g. localhost:8080
	Since  time.Time
	cancel context.CancelFunc
	cmd    *exec.Cmd
}

// Runner starts one quick tunnel and blocks until ctx ends, reporting the
// public URL once cloudflared prints it.
type Runner interface {
	Run(ctx context.Context, local string, out io.Writer) (string, error)
}

// BinaryRunner shells out to cloudflared.
type BinaryRunner struct {
	Bin string // empty = "cloudflared" on PATH
}

var quickURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Run starts `cloudflared tunnel --url <local>` and parses the public URL.
func (b BinaryRunner) Run(ctx context.Context, local string, out io.Writer) (string, error) {
	bin := b.Bin
	if bin == "" {
		bin = "cloudflared"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("gateway: cloudflared not installed (quick-share unavailable)")
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--url", local)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	deadline := time.Now().Add(30 * time.Second)
	for sc.Scan() {
		line := sc.Text()
		if out != nil {
			_, _ = io.WriteString(out, line+"\n")
		}
		if m := quickURL.FindString(line); m != "" {
			go func() {
				<-ctx.Done()
				_ = cmd.Wait()
			}()
			return m, nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return "", fmt.Errorf("gateway: cloudflared gave no URL in 30s")
		}
	}
	return "", fmt.Errorf("gateway: cloudflared exited before sharing")
}

// Registry tracks live shares.
type Registry struct {
	mu     sync.Mutex
	shares map[string]*Share
	runner Runner
}

// NewRegistry builds a share registry backed by runner.
func NewRegistry(runner Runner) *Registry {
	if runner == nil {
		runner = BinaryRunner{}
	}
	return &Registry{shares: map[string]*Share{}, runner: runner}
}

// Open shares local and returns the live share (idempotent per local: an
// already-shared local returns the existing share).
func (r *Registry) Open(ctx context.Context, id, local string) (*Share, error) {
	local = strings.TrimSpace(local)
	if id == "" || local == "" {
		return nil, fmt.Errorf("gateway: share needs id and local address")
	}
	r.mu.Lock()
	for _, s := range r.shares {
		if s.Local == local {
			defer r.mu.Unlock()
			return s, nil
		}
	}
	r.mu.Unlock()
	sctx, cancel := context.WithCancel(context.Background())
	url, err := r.runner.Run(sctx, local, io.Discard)
	if err != nil {
		cancel()
		return nil, err
	}
	_ = ctx // lifetime is registry-owned until Close
	s := &Share{ID: id, URL: url, Local: local, Since: time.Now(), cancel: cancel}
	r.mu.Lock()
	r.shares[id] = s
	r.mu.Unlock()
	return s, nil
}

// Close stops a share; unknown ids are no-ops (idempotent).
func (r *Registry) Close(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.shares[id]; ok {
		s.cancel()
		delete(r.shares, id)
	}
}

// List returns live shares, oldest first.
func (r *Registry) List() []Share {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Share, 0, len(r.shares))
	for _, s := range r.shares {
		out = append(out, *s)
	}
	return out
}
