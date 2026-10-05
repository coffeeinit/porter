// Metrics retention tiers (FCM-23): raw samples roll up 10-minute, hourly,
// then daily, with bounded retention so observability never bloats Postgres.
// Bucketing uses date_trunc semantics (portable to PG); SQLite strftime
// idioms from the reference are deliberately not copied.
package metrics

import (
	"fmt"
	"time"
)

// Tiers in rollup order with retention.
const (
	TierRaw    = "raw"    // 10s samples, kept 1h
	Tier10Min  = "10min"  // kept 7d
	TierHourly = "hourly" // kept 90d
	TierDaily  = "daily"  // kept 6mo
)

// Retention returns how long a tier is kept.
func Retention(tier string) (time.Duration, error) {
	switch tier {
	case TierRaw:
		return time.Hour, nil
	case Tier10Min:
		return 7 * 24 * time.Hour, nil
	case TierHourly:
		return 90 * 24 * time.Hour, nil
	case TierDaily:
		return 180 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("metrics: unknown tier %q", tier)
	}
}

// Bucket truncates t to the tier grain (date_trunc equivalent).
func Bucket(t time.Time, tier string) (time.Time, error) {
	switch tier {
	case TierRaw:
		return t, nil
	case Tier10Min:
		return t.Truncate(10 * time.Minute), nil
	case TierHourly:
		return t.Truncate(time.Hour), nil
	case TierDaily:
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, t.Location()), nil
	default:
		return time.Time{}, fmt.Errorf("metrics: unknown tier %q", tier)
	}
}
