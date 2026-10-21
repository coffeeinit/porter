package store

// Enrollment-token read helpers (WP8). Masking is hermetic; the round-trips
// run against the live Postgres (PORTER_TEST_DATABASE_URL) with fixtures
// removed via t.Cleanup — the DB is shared.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"porter/internal/agent"
)

// TestMaskEnrollmentToken: list views never leak the raw token body.
func TestMaskEnrollmentToken(t *testing.T) {
	full := "penr_" + strings.Repeat("ab", 32)
	if got := maskEnrollmentToken(full); strings.Contains(got, strings.Repeat("ab", 32)) {
		t.Fatalf("mask must not contain the raw token body: %q", got)
	}
	if got := maskEnrollmentToken(full); !strings.HasPrefix(got, "penr_…") {
		t.Fatalf("mask must keep the recognizable prefix, got %q", got)
	}
	if got := maskEnrollmentToken("short"); got != "penr_…" {
		t.Fatalf("short token must be fully masked, got %q", got)
	}
}

// TestEnrollmentTokenRowRoundTrip: Get returns the raw value plus state,
// List masks it, Delete removes it.
func TestEnrollmentTokenRowRoundTrip(t *testing.T) {
	dsn := os.Getenv("PORTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PORTER_TEST_DATABASE_URL not set; skipping enrollment token round trip")
	}
	st := NewStore(dsn)
	defer st.Close()

	minted, err := agent.NewEnrollmentToken("customer_owned", time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := st.CreateEnrollmentToken(minted.Token, "hetzner", `{"zone":"fsn1"}`, minted.ExpiresAt); err != nil {
		t.Fatalf("create token: %v", err)
	}
	t.Cleanup(func() { st.DeleteEnrollmentToken(minted.Token) })

	row, ok := st.GetEnrollmentToken(minted.Token)
	if !ok {
		t.Fatal("GetEnrollmentToken: token not found right after create")
	}
	if row.Token != minted.Token {
		t.Fatalf("GetEnrollmentToken must return the raw value, got %q", row.Token)
	}
	if row.Provider != "hetzner" || row.UsedBy != "" {
		t.Fatalf("unexpected row state: %+v", row)
	}
	// jsonb normalizes spacing; compare parsed, not raw text.
	var labels map[string]string
	if err := json.Unmarshal([]byte(row.Labels), &labels); err != nil || labels["zone"] != "fsn1" {
		t.Fatalf("labels round trip failed: %q (%v)", row.Labels, err)
	}
	if !row.ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("expires_at not preserved: %v", row.ExpiresAt)
	}

	if _, ok := st.GetEnrollmentToken("penr_" + strings.Repeat("0", 64)); ok {
		t.Fatal("unknown token must not resolve")
	}

	// Consume, then re-read: used_by surfaces through Get.
	if !st.ConsumeEnrollmentToken(minted.Token, "node-a") {
		t.Fatal("consume failed on a fresh token")
	}
	row, ok = st.GetEnrollmentToken(minted.Token)
	if !ok || row.UsedBy != "node-a" {
		t.Fatalf("consumed token must carry used_by=node-a, got %+v ok=%v", row, ok)
	}

	// List masks the value but keeps the row findable by suffix.
	listed := st.ListEnrollmentTokens()
	seen := false
	for _, l := range listed {
		if strings.HasSuffix(l.Token, minted.Token[len(minted.Token)-4:]) {
			seen = true
			if l.Token == minted.Token {
				t.Fatal("list leaked the raw enrollment token")
			}
			if l.UsedBy != "node-a" {
				t.Fatalf("list row used_by mismatch: %+v", l)
			}
		}
	}
	if !seen {
		t.Fatalf("masked token not found in list of %d rows", len(listed))
	}

	if !st.DeleteEnrollmentToken(minted.Token) {
		t.Fatal("delete must remove the token row")
	}
	if _, ok := st.GetEnrollmentToken(minted.Token); ok {
		t.Fatal("token still present after delete")
	}
}
