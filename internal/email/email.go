// Package email provides per-domain outbound mail (user-facing): each
// customer domain gets its own sender identity (From domain, DKIM selector
// + key reference), mail is queued through the workflow task ledger and
// delivered by a Sender transport (SMTP / webhook). Tenants never touch
// each other's identities, and no outside mail service is required —
// Porter is the mail system for the domains it hosts.
package email

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Sender delivers one message; transports adapt (SMTP, webhook, stub).
type Sender interface {
	Send(to []string, subject, body string) error
}

// DomainIdentity is one customer domain's mail identity.
type DomainIdentity struct {
	Domain       string // e.g. customer.com
	FromName     string // display name, e.g. "Acme"
	DKIMSelector string // e.g. porter1 (TXT at selector._domainkey)
	DKIMKeyRef   string // secret-store reference, never the key
	Enabled      bool
}

// From renders the RFC 5322 From header value.
func (d DomainIdentity) From(local string) string {
	if local == "" {
		local = "no-reply"
	}
	name := d.FromName
	if name == "" {
		name = d.Domain
	}
	return fmt.Sprintf("%s <%s@%s>", name, local, d.Domain)
}

// Validate gates identities: domain shape, DKIM pair complete.
func (d DomainIdentity) Validate() error {
	if !strings.Contains(d.Domain, ".") || strings.ContainsAny(d.Domain, " /\\") {
		return fmt.Errorf("email: bad domain %q", d.Domain)
	}
	if (d.DKIMSelector == "") != (d.DKIMKeyRef == "") {
		return fmt.Errorf("email: DKIM selector and key ref must be set together")
	}
	return nil
}

// Message is one queued outbound mail.
type Message struct {
	Domain  string // sender identity domain
	Local   string // local part, default no-reply
	To      []string
	Subject string
	Text    string
}

// Validate gates messages before queueing.
func (m Message) Validate() error {
	if m.Domain == "" || len(m.To) == 0 || m.Subject == "" {
		return fmt.Errorf("email: message needs domain, recipients and subject")
	}
	return nil
}

// Service routes per-domain mail with a per-domain rate cap.
type Service struct {
	mu         sync.Mutex
	identities map[string]DomainIdentity
	sent       map[string][]time.Time // domain -> send timestamps (windowed)
	perMin     int
	now        func() time.Time
	sender     Sender
}

// NewService builds a mail service delivering via sender, capped at
// perMin messages per domain per minute (<=0 = 60).
func NewService(sender Sender, perMin int) *Service {
	if perMin <= 0 {
		perMin = 60
	}
	return &Service{
		identities: map[string]DomainIdentity{},
		sent:       map[string][]time.Time{},
		perMin:     perMin,
		now:        time.Now,
		sender:     sender,
	}
}

// Register adds a sender identity (replaces same-domain).
func (s *Service) Register(id DomainIdentity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identities[strings.ToLower(id.Domain)] = id
	return nil
}

// Send validates, rate-checks, and delivers one message.
func (s *Service) Send(m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.identities[strings.ToLower(m.Domain)]
	if !ok || !id.Enabled {
		return fmt.Errorf("email: domain %q not enabled", m.Domain)
	}
	cutoff := s.now().Add(-time.Minute)
	kept := s.sent[id.Domain][:0]
	for _, ts := range s.sent[id.Domain] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	s.sent[id.Domain] = kept
	if len(kept) >= s.perMin {
		return fmt.Errorf("email: domain %q over rate cap", m.Domain)
	}
	body := m.Text + "\n\n--\nSent via " + id.Domain
	if err := s.sender.Send(m.To, m.Subject, body); err != nil {
		return err
	}
	s.sent[id.Domain] = append(s.sent[id.Domain], s.now())
	return nil
}
