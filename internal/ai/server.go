// MCP facade server (OCM-14 remainder): workspace-scoped connectors behind
// one HTTP surface as search → describe → call. Policy is enforced before
// anything runs; every call is audited. Execution dispatches to registered
// Executor backends per connector kind; kinds without an executor fail
// explicitly (501) instead of pretending. A diagnostic echo executor ships
// for connectivity checks and is allowlisted by exact kind name.
package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Executor runs one authorized call and returns a JSON-marshalable result.
type Executor func(call Call) (any, error)

// AuditFunc records one call (actor, address, policy decision, error).
type AuditFunc func(actor, address, decision, errMsg string)

// Server is the single MCP facade.
type Server struct {
	mu         sync.Mutex
	connectors map[string]Connector // by address
	executors  map[string]Executor  // by connector kind
	audit      AuditFunc
}

// NewServer builds an empty facade (no connectors, echo executor only).
func NewServer(audit AuditFunc) *Server {
	s := &Server{
		connectors: map[string]Connector{},
		executors:  map[string]Executor{},
		audit:      audit,
	}
	s.executors["diagnostic.echo"] = func(call Call) (any, error) {
		return map[string]string{"echo": call.Args["message"]}, nil
	}
	return s
}

// Register adds a connector (replaces same address).
func (s *Server) Register(c Connector) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectors[c.Address] = c
	return nil
}

// RegisterExecutor adds an execution backend for a connector kind
// (kind.name joined, e.g. "github.main" or "diagnostic.echo").
func (s *Server) RegisterExecutor(kind string, ex Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[kind] = ex
}

// Search lists connector addresses visible in a workspace (prefix match).
func (s *Server) Search(workspace, query string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for addr := range s.connectors {
		ws, _, _, err := ParseAddress(addr)
		if err != nil || (workspace != "" && ws != workspace) {
			continue
		}
		if query == "" || strings.Contains(addr, query) {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

// Describe returns one connector's coordinates and policy.
func (s *Server) Describe(address string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.connectors[address]
	if !ok {
		return nil, fmt.Errorf("ai: unknown connector %q", address)
	}
	ws, kind, name, _ := ParseAddress(address)
	return map[string]string{
		"address": address, "workspace": ws,
		"kind": kind, "name": name, "policy": c.Policy,
	}, nil
}

// kindOf extracts the executor key from an address (kind.name joined).
func kindOf(address string) string {
	_, kind, name, err := ParseAddress(address)
	if err != nil {
		return ""
	}
	return kind + "." + name
}

// Call authorizes, audits, and executes one tool call.
func (s *Server) Call(actor string, call Call) (any, error) {
	s.mu.Lock()
	c, ok := s.connectors[call.Address]
	ex, hasEx := s.executors[kindOf(call.Address)]
	audit := s.audit
	s.mu.Unlock()

	decide := func(decision, errMsg string) {
		if audit != nil {
			audit(actor, call.Address, decision, errMsg)
		}
	}
	if !ok {
		decide("deny", "unknown connector")
		return nil, fmt.Errorf("ai: unknown connector %q", call.Address)
	}
	if err := c.Authorize(call); err != nil {
		decide("deny", err.Error())
		return nil, err
	}
	if !hasEx {
		decide("deny", "no executor for kind")
		return nil, fmt.Errorf("ai: no executor for %q", call.Address)
	}
	res, err := ex(call)
	if err != nil {
		decide("error", err.Error())
		return nil, err
	}
	decide("allow", "")
	return res, nil
}

// ServeHTTP exposes search/describe/call as plain JSON (mounted by the API
// layer behind mcp.* capabilities; direct use is for tests).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	actor := r.Header.Get("X-Actor")
	switch r.URL.Path {
	case "/search":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tools": s.Search(r.URL.Query().Get("workspace"), r.URL.Query().Get("q")),
		})
	case "/describe":
		d, err := s.Describe(r.URL.Query().Get("address"))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(d)
	case "/call":
		var call Call
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad call body"})
			return
		}
		res, err := s.Call(actor, call)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": res})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown mcp path"})
	}
}
