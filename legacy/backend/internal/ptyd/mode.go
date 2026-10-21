// Porter ptyd mode: guest-side terminal daemon (OCM-17).
//
// Session registry on HTTP plus pipe-backed command execution on
// 127.0.0.1:7681. No SSH, no PTY allocation: commands run via os/exec with
// pipes, which is honest about what it is (fully working, labeled); a real
// PTY upgrade needs golang.org/x/term on the guest image. Token-gated:
// empty PORTER_PTYD_TOKEN fails closed, mirroring the agent gates.
package ptyd

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func Run() int {
	addr := envOr("PORTER_PTYD_ADDR", fmt.Sprintf("127.0.0.1:%d", Port))

	// Fail closed: no token, no daemon. Per-request ptydGate already
	// rejects with 500/401, but binding the port without a token would
	// advertise a terminal surface that can never authenticate — exit
	// non-zero before serving anything, mirroring the agent gates.
	if os.Getenv("PORTER_PTYD_TOKEN") == "" {
		log.Fatalf("ptyd: PORTER_PTYD_TOKEN is required (fail-closed)")
	}

	reg := NewRegistry(4)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var s Session
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writePtydJSON(w, http.StatusBadRequest, map[string]string{"error": "bad session body"})
			return
		}
		if s.OpenedAt.IsZero() {
			s.OpenedAt = time.Now()
		}
		if err := reg.Open(s); err != nil {
			writePtydJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writePtydJSON(w, http.StatusCreated, map[string]string{"id": s.ID})
	})
	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		reg.Close(r.PathValue("id")) // idempotent: unknown IDs are a no-op
		writePtydJSON(w, http.StatusOK, map[string]string{"status": "closed"})
	})
	mux.HandleFunc("POST /exec", func(w http.ResponseWriter, r *http.Request) {
		// Pipe-backed exec (no PTY): stdout/stderr are captured buffers, not
		// a terminal — interactive curses-style programs will not work.
		var req struct {
			Session string   `json:"session"`
			Argv    []string `json:"argv"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Argv) == 0 {
			writePtydJSON(w, http.StatusBadRequest, map[string]string{"error": "argv required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				writePtydJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("exec: %v", err)})
				return
			}
		}
		writePtydJSON(w, http.StatusOK, map[string]any{
			"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": code,
		})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writePtydJSON(w, http.StatusOK, map[string]string{"status": "ok", "backend": "pipes"})
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           ptydGate(mux),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("porter ptyd on %s (pipe-backed sessions)", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ptyd server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && err != http.ErrServerClosed {
		log.Printf("ptyd shutdown error: %v", err)
	}
	log.Printf("ptyd: stopped")
	return 0
}

// ptydGate enforces PORTER_PTYD_TOKEN from the X-Ptyd-Token header. Empty
// configured token fails closed (500 for every request); mismatches are 401.
func ptydGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := os.Getenv("PORTER_PTYD_TOKEN")
		if want == "" {
			http.Error(w, "ptyd disabled", http.StatusInternalServerError)
			return
		}
		got := r.Header.Get("X-Ptyd-Token")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writePtydJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
