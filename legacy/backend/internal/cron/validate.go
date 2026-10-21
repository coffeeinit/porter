// Package cron — cron expression validation.
//
// Validate reports whether expr is a syntactically valid 5-field schedule
// (minute hour day-of-month month day-of-week) for the Matches grammar in
// runner.go: "*", ranges (1-5), steps (*/2, 1-10/2), and comma lists, with
// each numeric bound inside the same per-field ranges Matches enforces.
package cron

import (
	"strconv"
	"strings"
)

// validateRanges mirrors the (min, max) bounds Matches passes to fieldMatch,
// in field order: minute hour dom month dow.
var validateRanges = [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// Validate reports whether expr parses as a 5-field cron schedule.
func Validate(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	for i, f := range fields {
		if !validField(f, validateRanges[i][0], validateRanges[i][1]) {
			return false
		}
	}
	return true
}

// validField checks one comma-separated field against its range.
func validField(field string, min, max int) bool {
	if field == "" {
		return false
	}
	if field == "*" {
		return true
	}
	for _, part := range strings.Split(field, ",") {
		if !validPart(part, min, max) {
			return false
		}
	}
	return true
}

// validPart handles one comma-separated part: "*", "a-b", "*/n", "a-b/n",
// "v/n", or a plain value — the same grammar matchPart accepts.
func validPart(part string, min, max int) bool {
	if part == "" {
		return false
	}
	base := part
	if i := strings.IndexByte(part, '/'); i >= 0 {
		base = part[:i]
		s, err := strconv.Atoi(part[i+1:])
		if err != nil || s <= 0 {
			return false
		}
		if base == "" {
			return false
		}
	}
	switch {
	case base == "*":
		return true
	case strings.Contains(base, "-"):
		ps := strings.SplitN(base, "-", 2)
		a, err1 := strconv.Atoi(ps[0])
		b, err2 := strconv.Atoi(ps[1])
		if err1 != nil || err2 != nil {
			return false
		}
		if a < min || a > max || b < min || b > max {
			return false
		}
		return a <= b
	default:
		v, err := strconv.Atoi(base)
		if err != nil {
			return false
		}
		return v >= min && v <= max
	}
}
