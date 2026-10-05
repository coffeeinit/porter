package event

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBroadcastDelivers(t *testing.T) {
	h := NewHub()
	ch := make(chan sseEvent, 1)
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()

	h.Broadcast("vm.state", map[string]string{"id": "x"})
	select {
	case ev := <-ch:
		if ev.Event != "vm.state" {
			t.Fatalf("unexpected event %q", ev.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
}

func TestSlowClientDoesNotBlockBroadcast(t *testing.T) {
	h := NewHub()
	ch := make(chan sseEvent, 1)
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()
	ch <- sseEvent{} // fill the buffer so the next broadcast would block

	done := make(chan struct{})
	go func() {
		h.Broadcast("x", "y")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on slow client")
	}
}

func TestServeHTTPStreamsSSE(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	h.Broadcast("hello", map[string]string{"a": "b"})
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: hello") {
		t.Fatalf("SSE body missing event, got: %q", body)
	}
	if !strings.Contains(body, `"a":"b"`) {
		t.Fatalf("SSE body missing payload, got: %q", body)
	}
}
