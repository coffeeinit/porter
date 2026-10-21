package cron

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	valid := []string{
		"* * * * *",
		"0 0 * * *",
		"*/15 9-17 * * 1-5",
		"0 0 1 1 0",
		"0 0 * * 7",
		"5,10,15 * * * *",
		"30 2 1-15/2 * *",
	}
	for _, expr := range valid {
		if !Validate(expr) {
			t.Errorf("Validate(%q) = false, want true", expr)
		}
	}
	invalid := []string{
		"",
		"bad",
		"* * * *",       // 4 fields
		"* * * * * *",   // 6 fields
		"61 * * * *",    // minute out of range
		"* 24 * * *",    // hour out of range
		"* * 0 * *",     // dom below range
		"* * 32 * *",    // dom above range
		"* * * 13 *",    // month out of range
		"* * * * 8",     // dow out of range
		"*/0 * * * *",   // non-positive step
		"*/a * * * *",   // non-numeric step
		"1-2-3 * * * *", // malformed range
		"5/ * * * *",    // empty step
		"/2 * * * *",    // empty base
		", * * * *",     // empty list element
		"MON * * * *",   // names unsupported
		"5-2 * * * *",   // inverted range
		"* * * * * ",    // trailing space is fine — Fields trims; keep as valid check below
	}
	// The last entry is actually valid (Fields trims whitespace); check separately.
	if !Validate(invalid[len(invalid)-1]) {
		t.Errorf("Validate(%q) = false, want true (whitespace trimmed)", invalid[len(invalid)-1])
	}
	for _, expr := range invalid[:len(invalid)-1] {
		if Validate(expr) {
			t.Errorf("Validate(%q) = true, want false", expr)
		}
	}
}

// TestValidateMatchesAgree spot-checks that every single-value valid
// expression matches its own time via Matches.
func TestValidateMatchesAgree(t *testing.T) {
	fixed := time.Date(2026, 8, 14, 9, 42, 0, 0, time.UTC) // Friday
	expr := "42 9 14 8 5"
	if !Validate(expr) {
		t.Fatalf("Validate(%q) = false, want true", expr)
	}
	if !Matches(expr, fixed) {
		t.Fatalf("Matches(%q) = false, want true", expr)
	}
	if Validate("42 10 * * *") && Matches("42 10 * * *", fixed) {
		t.Fatal("mismatched hour should not match")
	}
}
