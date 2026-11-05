package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestClaimWebhookDeliveryIsIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	claimed, err := s.ClaimWebhookDelivery(ctx, "GitHub", "delivery-1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v; want true, nil", claimed, err)
	}
	claimed, err = s.ClaimWebhookDelivery(ctx, "github", "delivery-1")
	if err != nil || claimed {
		t.Fatalf("duplicate claim = %v, %v; want false, nil", claimed, err)
	}
	claimed, err = s.ClaimWebhookDelivery(ctx, "github", "delivery-2")
	if err != nil || !claimed {
		t.Fatalf("second delivery claim = %v, %v; want true, nil", claimed, err)
	}
}

func TestClaimWebhookDeliveryRequiresIdentity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "porter.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	for _, tc := range []struct{ provider, id string }{{"", "delivery"}, {"github", ""}} {
		if claimed, err := s.ClaimWebhookDelivery(context.Background(), tc.provider, tc.id); err == nil || claimed {
			t.Fatalf("invalid identity (%q,%q) = %v, %v; want error", tc.provider, tc.id, claimed, err)
		}
	}
}
