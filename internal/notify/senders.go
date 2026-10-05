// Chat-ops notifiers (OCM-25): SMTP mail exists (Mailer); this file adds
// webhook senders for Telegram and Slack-compatible endpoints behind one
// Sender interface so alert/incident escalation fans out uniformly.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Sender delivers a message; implementations are transport adapters.
type Sender interface {
	Send(to []string, subject, body string) error
}

// WebhookSender POSTs JSON payloads (Telegram Bot API / Slack incoming
// webhooks) with a bounded timeout.
type WebhookSender struct {
	URL    string
	Kind   string // telegram | slack
	Token  string // bot token for telegram; empty for slack webhooks
	Client *http.Client
}

// Validate gates sender config.
func (w WebhookSender) Validate() error {
	if w.URL == "" {
		return fmt.Errorf("notify: webhook needs a URL")
	}
	switch w.Kind {
	case "telegram", "slack":
	default:
		return fmt.Errorf("notify: unknown webhook kind %q", w.Kind)
	}
	if w.Kind == "telegram" && w.Token == "" {
		return fmt.Errorf("notify: telegram needs a bot token")
	}
	return nil
}

func (w WebhookSender) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Send posts subject+body; to is the chat/channel id (one entry used).
func (w WebhookSender) Send(to []string, subject, body string) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if len(to) == 0 {
		return fmt.Errorf("notify: webhook needs a destination")
	}
	var payload any
	switch w.Kind {
	case "telegram":
		payload = map[string]string{"chat_id": to[0], "text": subject + "\n" + body}
	default:
		payload = map[string]string{"text": "*" + subject + "*\n" + body}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, w.URL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Kind == "telegram" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	}
	resp, err := w.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook %s -> status %d", w.Kind, resp.StatusCode)
	}
	return nil
}
