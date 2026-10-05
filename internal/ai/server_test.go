package ai

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFacadeFlow(t *testing.T) {
	var audits int
	s := NewServer(func(_, _, _, _ string) { audits++ })
	if err := s.Register(Connector{Address: "wi.acme.github.main", Policy: PolicyAllow}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(Connector{Address: "wi.acme.deploy.prod", Policy: PolicyRequireApproval}); err != nil {
		t.Fatal(err)
	}
	if got := s.Search("acme", "github"); len(got) != 1 {
		t.Fatalf("search wrong: %v", got)
	}
	if _, err := s.Describe("wi.nope.x.y"); err == nil {
		t.Fatal("unknown describe must fail")
	}
	// Approval gate first.
	if _, err := s.Call("u1", Call{Address: "wi.acme.deploy.prod"}); err == nil {
		t.Fatal("unapproved require_approval must fail")
	}
	if _, err := s.Call("u1", Call{Address: "wi.acme.deploy.prod", Approved: true, ApprovedBy: "u1"}); err == nil {
		t.Fatal("no executor must fail explicitly")
	}
	// Echo executor proves the plumbing end to end.
	if err := s.Register(Connector{Address: "wi.acme.diagnostic.echo", Policy: PolicyAllow}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Call("u1", Call{Address: "wi.acme.diagnostic.echo", Args: map[string]string{"message": "ping"}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]string)
	if m["echo"] != "ping" {
		t.Fatal("echo wrong")
	}
	if audits != 3 {
		t.Fatalf("every call audited, got %d", audits)
	}
}

func TestFacadeHTTP(t *testing.T) {
	s := NewServer(nil)
	_ = s.Register(Connector{Address: "wi.acme.github.main", Policy: PolicyAllow})
	req := httptest.NewRequest("GET", "/search?workspace=acme", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "wi.acme.github.main") {
		t.Fatal("search http wrong:", rec.Body.String())
	}
}
