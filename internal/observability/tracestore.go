// Trace/span/feedback store (OCM-24): the cheapest AI-ops observability to
// own — durable traces with spans plus human/AI feedback scores on
// operations. Transport-agnostic (OTel-compatible field names); the store
// interface lets Postgres back it while tests run in memory.
package observability

import (
	"fmt"
	"sync"
)

// Trace is one distributed operation trace.
type Trace struct {
	ID        string
	Service   string
	Operation string
}

// Span is one timed unit inside a trace.
type Span struct {
	TraceID   string
	SpanID    string
	ParentID  string
	Name      string
	DurationM int64
	Error     string
}

// FeedbackScore rates one operation (human or AI judge feedback).
type FeedbackScore struct {
	TraceID string
	Name    string // e.g. correctness, latency-ok
	Value   float64
}

// Validate gates feedback: 0..1 range, identity required.
func (f FeedbackScore) Validate() error {
	if f.TraceID == "" || f.Name == "" {
		return fmt.Errorf("observability: feedback needs trace id and name")
	}
	if f.Value < 0 || f.Value > 1 {
		return fmt.Errorf("observability: feedback value %v out of [0,1]", f.Value)
	}
	return nil
}

// TraceStore persists traces, spans, and scores.
type TraceStore interface {
	PutTrace(Trace) error
	PutSpan(Span) error
	PutFeedback(FeedbackScore) error
	Spans(traceID string) []Span
}

// MemoryTraceStore is the in-memory implementation (tests, dev).
type MemoryTraceStore struct {
	mu       sync.Mutex
	traces   map[string]Trace
	spans    map[string][]Span
	feedback []FeedbackScore
}

// NewMemoryTraceStore builds an empty store.
func NewMemoryTraceStore() *MemoryTraceStore {
	return &MemoryTraceStore{traces: map[string]Trace{}, spans: map[string][]Span{}}
}

func (m *MemoryTraceStore) PutTrace(t Trace) error {
	if t.ID == "" {
		return fmt.Errorf("observability: trace needs an id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traces[t.ID] = t
	return nil
}

func (m *MemoryTraceStore) PutSpan(s Span) error {
	if s.TraceID == "" || s.SpanID == "" {
		return fmt.Errorf("observability: span needs trace and span ids")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans[s.TraceID] = append(m.spans[s.TraceID], s)
	return nil
}

func (m *MemoryTraceStore) PutFeedback(f FeedbackScore) error {
	if err := f.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedback = append(m.feedback, f)
	return nil
}

// Spans returns spans for a trace in insertion order.
func (m *MemoryTraceStore) Spans(traceID string) []Span {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Span(nil), m.spans[traceID]...)
}
