// Package notify delivers out-of-band notifications (currently email via a
// simple SMTP client) for alerts and operational events. It uses only the
// standard library so `porter` keeps its zero-new-module build.
package notify

import (
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig carries the mail settings (from [notify] in porter.toml).
type SMTPConfig struct {
	Host      string // e.g. smtp.example.com
	Port      int    // e.g. 587
	User      string // SMTP AUTH user (may be empty for open relays)
	Password  string
	From      string // From address, e.g. "porter@example.com"
	DefaultTo string // recipients when an alert has no explicit destination
	Enabled   bool
}

// Mailer sends alert/notification email through SMTP.
type Mailer struct {
	cfg SMTPConfig
}

// New returns a Mailer. It is harmless to keep a disabled one around; Send
// returns nil without doing work when cfg.Enabled is false.
func New(cfg SMTPConfig) *Mailer { return &Mailer{cfg: cfg} }

// Enable reports whether real mail will be sent.
func (m *Mailer) Enable() bool { return m.cfg.Enabled && m.cfg.Host != "" }

// Send delivers a plain-text email to to (a comma-separated list allowed).
func (m *Mailer) Send(to []string, subject, body string) error {
	if !m.Enable() {
		return nil
	}
	if len(to) == 0 {
		to = []string{m.cfg.DefaultTo}
	}
	if len(to) == 0 || to[0] == "" {
		return fmt.Errorf("notify: no recipient configured")
	}
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)
	auth := smtp.PlainAuth("", m.cfg.User, m.cfg.Password, m.cfg.Host)

	msg := buildMessage(m.cfg.From, to, subject, body)
	err := smtp.SendMail(addr, auth, m.cfg.From, to, []byte(msg))
	if err != nil {
		return fmt.Errorf("notify: smtp %s: %w", addr, err)
	}
	return nil
}

// SendAlert is a convenience for the common alert payload.
func (m *Mailer) SendAlert(to, project, alertID, status, detail string) error {
	return m.Send([]string{to},
		fmt.Sprintf("[porter] alert %s on %s: %s", status, project, alertID),
		fmt.Sprintf("Project: %s\nAlert: %s\nStatus: %s\nDetail: %s\nSent: %s\n",
			project, alertID, status, detail, time.Now().Format(time.RFC3339)))
}

// buildMessage assembles a minimal RFC-5322 message.
func buildMessage(from string, to []string, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return b.String()
}
