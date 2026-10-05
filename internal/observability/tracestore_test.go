package observability

import "testing"

func TestTraceStore(t *testing.T) {
	s := NewMemoryTraceStore()
	if err := s.PutTrace(Trace{ID: "t1", Service: "api", Operation: "deploy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSpan(Span{TraceID: "t1", SpanID: "s1", Name: "build"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Spans("t1"); len(got) != 1 {
		t.Fatal("span must persist")
	}
	if err := s.PutFeedback(FeedbackScore{TraceID: "t1", Name: "correctness", Value: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFeedback(FeedbackScore{TraceID: "t1", Name: "x", Value: 2}); err == nil {
		t.Fatal("out-of-range feedback must fail")
	}
}
