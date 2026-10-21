package cron

import (
	"testing"
	"time"
)

func TestMatches(t *testing.T) {
	// A fixed time: 2026-08-14 09:42:00 (Friday).
	fixed := time.Date(2026, 8, 14, 9, 42, 0, 0, time.UTC)
	tests := []struct {
		expr string
		want bool
	}{
		{"42 9 14 8 5", true},   // exact
		{"42 9 14 8 0", false},  // 0 = Sunday, fixed day is Friday (5)
		{"42 9 * * *", true},    // every day
		{"* * * * *", true},     // every minute
		{"*/2 * * * *", true},   // 42 % 2 == 0
		{"*/5 * * * *", false},  // 42 % 5 != 0
		{"0 9 14 8 5", false},   // wrong minute
		{"42 10 * * *", false},  // wrong hour
		{"1-30 * * * *", false}, // 42 outside range
		{"bad", false},          // invalid
	}
	for _, tt := range tests {
		if got := Matches(tt.expr, fixed); got != tt.want {
			t.Errorf("Matches(%q, %v) = %v, want %v", tt.expr, fixed, got, tt.want)
		}
	}
}

func TestFieldMatch(t *testing.T) {
	if !fieldMatch("*", "5", 0, 59) {
		t.Error("star should match")
	}
	if !fieldMatch("1-5", "3", 0, 59) {
		t.Error("range should match")
	}
	if fieldMatch("1-5", "9", 0, 59) {
		t.Error("range should not match outside")
	}
	if !fieldMatch("*/10", "20", 0, 59) {
		t.Error("step should match")
	}
	if fieldMatch("*/10", "25", 0, 59) {
		t.Error("step should not match offset")
	}
	if !fieldMatch("1,3,5", "3", 0, 59) {
		t.Error("list should match")
	}
}
