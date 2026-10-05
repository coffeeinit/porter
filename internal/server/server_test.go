package server

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the stdlib logger for the duration of one test and
// returns whatever it wrote (the join helper reports stragglers via
// log.Printf).
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestWatchGroupJoinsHealthyWatchers pins the healthy path: stub watchers
// that return promptly on ctx cancellation are all joined well inside the
// budget (no straggler lines logged).
func TestWatchGroupJoinsHealthyWatchers(t *testing.T) {
	logBuf := captureLog(t)
	g := newWatchGroup()
	ctx, cancel := context.WithCancel(context.Background())
	for _, name := range []string{"meter-pump", "fc-collector", "sshgw", "health/vm-1"} {
		g.spawn(ctx, name, func(ctx context.Context) {
			<-ctx.Done() // the real watchers exit on cancellation
		})
	}
	cancel()
	if !g.join(5 * time.Second) {
		t.Fatalf("join timed out; watchers did not exit on cancellation")
	}
	if out := logBuf.String(); strings.Contains(out, "did not exit") {
		t.Fatalf("healthy join must not log stragglers, got: %s", out)
	}
}

// TestWatchGroupTimesOutAndLogsStragglers pins the bounded-wait contract: a
// watcher that stays stuck past cancellation must not hold the join beyond
// the timeout, and it must be named in the log line.
func TestWatchGroupTimesOutAndLogsStragglers(t *testing.T) {
	logBuf := captureLog(t)
	g := newWatchGroup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	g.spawn(ctx, "meter-pump", func(ctx context.Context) {
		<-ctx.Done()
		<-release // simulates a watcher stuck in a syscall past cancellation
	})
	g.spawn(ctx, "fc-collector", func(ctx context.Context) {
		<-ctx.Done() // exits cleanly
	})
	cancel()

	start := time.Now()
	if g.join(100 * time.Millisecond) {
		close(release)
		t.Fatal("join must report false when a watcher misses the deadline")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("join waited %s for a straggler; must be bounded", elapsed)
	}
	if !strings.Contains(logBuf.String(), `watcher "meter-pump" did not exit within 100ms`) {
		t.Fatalf("straggler not named in log, got: %s", logBuf.String())
	}

	// After release, a second join succeeds: exit is reported once it lands.
	close(release)
	if !g.join(5 * time.Second) {
		t.Fatal("released watcher must be joinable afterwards")
	}
}

// TestWatchGroupEmpty pins the no-watchers fast path (e.g. health/ssh disabled).
func TestWatchGroupEmpty(t *testing.T) {
	g := newWatchGroup()
	if !g.join(time.Millisecond) {
		t.Fatal("empty group must join immediately")
	}
}
