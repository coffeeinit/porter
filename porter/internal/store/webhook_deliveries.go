package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ClaimWebhookDelivery records a provider delivery ID exactly once. A duplicate
// authenticated webhook is safe to acknowledge without starting a second build.
func (s *Store) ClaimWebhookDelivery(ctx context.Context, provider, deliveryID string) (bool, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	deliveryID = strings.TrimSpace(deliveryID)
	if provider == "" || deliveryID == "" {
		return false, errors.New("provider and delivery id are required")
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS webhook_deliveries (
		provider TEXT NOT NULL,
		delivery_id TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY(provider, delivery_id)
	)`); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO webhook_deliveries(provider,delivery_id,created_at) VALUES(?,?,?)`,
		provider, deliveryID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
