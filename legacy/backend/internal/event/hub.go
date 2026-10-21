// Package event implements a small Server-Sent-Events hub: broadcast an
// event+payload to all connected dashboard streams.
package event

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type sseEvent struct {
	Event string
	Data  any
}

// Hub fans out broadcasts to every connected SSE client.
type Hub struct {
	mu      sync.Mutex
	clients map[chan sseEvent]bool
}

func NewHub() *Hub {
	return &Hub{clients: map[chan sseEvent]bool{}}
}

// Broadcast sends event with payload to every connected client. Slow
// clients have events dropped rather than blocking the broadcaster.
func (h *Hub) Broadcast(event string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- sseEvent{Event: event, Data: data}:
		default:
			// slow client, drop event rather than block the broadcaster
		}
	}
}

// ServeHTTP implements the GET /events streaming endpoint.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan sseEvent, 32)
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		close(ch)
	}()

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case ev := <-ch:
			payload, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, payload)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
