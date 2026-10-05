package email

import (
	"testing"
)

type stubSender struct {
	sent int
	err  error
}

func (s *stubSender) Send([]string, string, string) error {
	s.sent++
	return s.err
}

func TestSendFlow(t *testing.T) {
	stub := &stubSender{}
	svc := NewService(stub, 2)
	if err := svc.Register(DomainIdentity{Domain: "acme.com", FromName: "Acme", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	m := Message{Domain: "acme.com", To: []string{"u@x.io"}, Subject: "hi", Text: "hello"}
	if err := svc.Send(m); err != nil {
		t.Fatal(err)
	}
	if err := svc.Send(m); err != nil {
		t.Fatal(err)
	}
	if err := svc.Send(m); err == nil {
		t.Fatal("rate cap must trip")
	}
	if stub.sent != 2 {
		t.Fatal("transport must see exactly 2")
	}
	if err := svc.Send(Message{Domain: "evil.com", To: []string{"u@x.io"}, Subject: "s", Text: "t"}); err == nil {
		t.Fatal("unregistered domain must fail")
	}
}

func TestIdentityValidate(t *testing.T) {
	if err := (DomainIdentity{Domain: "bad", Enabled: true}).Validate(); err == nil {
		t.Fatal("bad domain must fail")
	}
	if err := (DomainIdentity{Domain: "a.com", DKIMSelector: "s", Enabled: true}).Validate(); err == nil {
		t.Fatal("half DKIM must fail")
	}
}
