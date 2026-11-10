package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Example.COM:443", "example.com"},
		{"EXAMPLE.com.", "example.com"},
		{"[2001:db8::1]:8080", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
	} {
		if got := normalizeHost(tc.input); got != tc.want {
			t.Errorf("normalizeHost(%q)=%q want %q", tc.input, got, tc.want)
		}
	}
}

func TestStripPathPrefix(t *testing.T) {
	for _, tc := range []struct {
		path, prefix, want string
		ok                 bool
	}{
		{"/api/users", "/api", "/users", true},
		{"/api", "/api", "/", true},
		{"/api/", "/api/", "/", true},
		{"/apix", "/api", "", false},
		{"/other", "/api", "", false},
		{"/anything", "/", "/anything", true},
		{"/anything", "", "/anything", true},
		{"/api", "api", "", false},
	} {
		got, ok := stripPathPrefix(tc.path, tc.prefix)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stripPathPrefix(%q,%q)=(%q,%v) want (%q,%v)", tc.path, tc.prefix, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidTraceID(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"trace-01.a_b", true},
		{"", false},
		{"bad id", false},
		{"bad\nheader", false},
		{string(make([]byte, 129)), false},
	} {
		if got := validTraceID(tc.value); got != tc.want {
			t.Errorf("validTraceID(%q)=%v want %v", tc.value, got, tc.want)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestInstrumentedTransportPropagatesW3CTraceContext(t *testing.T) {
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(previousPropagator)

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("parse trace ID: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("parse span ID: %v", err)
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		Remote:     true,
		TraceFlags: trace.FlagsSampled,
	})
	request := httptest.NewRequest(http.MethodGet, "http://upstream.test/health", nil)
	request = request.WithContext(trace.ContextWithRemoteSpanContext(context.Background(), parent))

	var outgoingTraceparent string
	base := roundTripperFunc(func(outgoing *http.Request) (*http.Response, error) {
		outgoingTraceparent = outgoing.Header.Get("traceparent")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    outgoing,
		}, nil
	})
	response, err := newInstrumentedTransport(base).RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.HasPrefix(outgoingTraceparent, "00-"+traceID.String()+"-") {
		t.Fatalf("outgoing traceparent = %q, want trace ID %s", outgoingTraceparent, traceID)
	}
}
