package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"testing"
	"time"
)

// listenUnixOrSkip binds a unix socket for FC API tests, skipping when the
// host cannot bind unix sockets (e.g. Windows sandboxes). The Firecracker
// contract under test is OS-independent; socket availability is not.
func listenUnixOrSkip(t *testing.T, sock string) net.Listener {
	t.Helper()
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable on %s, skipping: %v", goruntime.GOOS, err)
	}
	return listener
}

func TestFCClientUsesUnixSocketAndOfficialPayload(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "firecracker.sock")
	listener := listenUnixOrSkip(t, sock)
	defer listener.Close()
	seen := make(chan map[string]any, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		seen <- body
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	client := newFCClient(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.SetMachineConfig(ctx, 2, 512); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-seen:
		if body["vcpu_count"] != float64(2) || body["mem_size_mib"] != float64(512) {
			t.Fatalf("unexpected Firecracker payload: %#v", body)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Unix-socket request")
	}
}

func TestWaitForSocketHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := waitForSocket(ctx, filepath.Join(t.TempDir(), "missing.sock")); err == nil {
		t.Fatal("waitForSocket returned nil for a missing socket")
	}
}

func TestFCClientSnapshotRequestsUseOfficialAPI(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "firecracker.sock")
	listener := listenUnixOrSkip(t, sock)
	defer listener.Close()

	type request struct {
		method string
		path   string
		body   map[string]any
	}
	seen := make(chan request, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		seen <- request{method: r.Method, path: r.URL.Path, body: body}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	client := newFCClient(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.SetState(ctx, "Paused"); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateSnapshot(ctx, "/snap/state", "/snap/memory"); err != nil {
		t.Fatal(err)
	}
	if err := client.LoadSnapshot(ctx, "/snap/state", "/snap/memory", false); err != nil {
		t.Fatal(err)
	}
	if err := client.SetState(ctx, "Resumed"); err != nil {
		t.Fatal(err)
	}

	expected := []request{
		{method: http.MethodPatch, path: "/vm", body: map[string]any{"state": "Paused"}},
		{method: http.MethodPut, path: "/snapshot/create", body: map[string]any{"snapshot_type": "Full", "snapshot_path": "/snap/state", "mem_file_path": "/snap/memory"}},
		{method: http.MethodPut, path: "/snapshot/load", body: map[string]any{"snapshot_path": "/snap/state", "mem_backend": map[string]any{"backend_path": "/snap/memory", "backend_type": "File"}, "track_dirty_pages": false, "resume_vm": false}},
		{method: http.MethodPatch, path: "/vm", body: map[string]any{"state": "Resumed"}},
	}
	for _, want := range expected {
		select {
		case got := <-seen:
			if got.method != want.method || got.path != want.path {
				t.Fatalf("unexpected Firecracker request: got %s %s, want %s %s", got.method, got.path, want.method, want.path)
			}
			if !reflect.DeepEqual(got.body, want.body) {
				t.Fatalf("unexpected %s payload: got %#v, want %#v", got.path, got.body, want.body)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s request", want.path)
		}
	}
}
