// Package billing is the commercial lifecycle. DEFERRED by rule: no tables
// until subscriptions ship. Meters ride store.RecordUsage into usage_events
// (migration 0022); rating and invoicing come later.
package billing
