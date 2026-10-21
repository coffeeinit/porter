// Postgres-backed TraceStore (migration 0029). Lives in store (which
// already depends on observability for the pgx tracer) so the dependency
// stays one-directional: store → observability, never the reverse.
package store

import (
	"porter/internal/observability"
)

// PGTraceStore persists traces/spans/feedback into Postgres.
type PGTraceStore struct {
	st *Store
}

// NewPGTraceStore wires the durable store (nil store = disabled, methods
// no-op so callers stay nil-safe before migrations run).
func NewPGTraceStore(st *Store) *PGTraceStore {
	return &PGTraceStore{st: st}
}

// PutTrace records one trace row (idempotent by primary key).
func (p *PGTraceStore) PutTrace(t observability.Trace) error {
	if p.st == nil {
		return nil
	}
	if t.ID == "" {
		return errNoTraceID
	}
	p.st.InsertTrace(t.ID, t.Service, t.Operation)
	return nil
}

// PutSpan appends one span row.
func (p *PGTraceStore) PutSpan(s observability.Span) error {
	if p.st == nil {
		return nil
	}
	if s.TraceID == "" || s.SpanID == "" {
		return errNoSpanID
	}
	p.st.InsertSpan(s.TraceID, s.SpanID, s.ParentID, s.Name, s.DurationM, s.Error)
	return nil
}

// PutFeedback validates and appends one feedback score.
func (p *PGTraceStore) PutFeedback(f observability.FeedbackScore) error {
	if p.st == nil {
		return nil
	}
	if err := f.Validate(); err != nil {
		return err
	}
	p.st.InsertFeedback(f.TraceID, f.Name, f.Value)
	return nil
}

// Spans returns spans for a trace in insertion order.
func (p *PGTraceStore) Spans(traceID string) []observability.Span {
	if p.st == nil {
		return nil
	}
	var out []observability.Span
	for _, row := range p.st.TraceSpans(traceID) {
		id, _ := row["span_id"].(string)
		parent, _ := row["parent_id"].(string)
		name, _ := row["name"].(string)
		ms, _ := row["duration_ms"].(int64)
		errMsg, _ := row["error"].(string)
		out = append(out, observability.Span{TraceID: traceID, SpanID: id, ParentID: parent, Name: name, DurationM: ms, Error: errMsg})
	}
	return out
}
