package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type exportRequest struct {
	path        string
	contentType string
	body        []byte
}

func TestSetupTracingExportsSanitizedW3CSpansOverOTLPHTTP(t *testing.T) {
	received := make(chan exportRequest, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OTLP request: %v", err)
		}
		select {
		case received <- exportRequest{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", strings.TrimRight(collector.URL, "/")+"/v1/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
	t.Setenv("OTEL_SERVICE_NAME", "porter-test")

	shutdown, err := SetupTracing(context.Background())
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)
		}
	})

	var seenPath, seenQuery, seenHost, seenRemoteAddr, seenUserAgent string
	var outboundURL, outboundTraceparent, outboundBaggage, outboundUserAgent string
	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenQuery, seenHost = r.URL.Path, r.URL.RawQuery, r.Host
		seenRemoteAddr, seenUserAgent = r.RemoteAddr, r.UserAgent()

		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://private-target.example/internal/client-secret?api_key=private-query-secret", nil)
		if err != nil {
			t.Errorf("create upstream request: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		request.Header.Set("User-Agent", "private-client-fingerprint")
		client := &http.Client{Transport: HTTPTransport(roundTripperFunc(func(outgoing *http.Request) (*http.Response, error) {
			outboundURL = outgoing.URL.String()
			outboundTraceparent = outgoing.Header.Get("traceparent")
			outboundBaggage = outgoing.Header.Get("baggage")
			outboundUserAgent = outgoing.UserAgent()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    outgoing,
			}, nil
		}))}
		response, err := client.Do(request)
		if err != nil {
			t.Errorf("upstream request: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = response.Body.Close()
		w.WriteHeader(http.StatusNoContent)
	}), "porter.test")

	request := httptest.NewRequest(http.MethodGet, "http://porter.local/private/server-secret?access_token=private-server-query", nil)
	request.Host = "customer-private.example"
	request.RemoteAddr = "203.0.113.9:4321"
	request.Header.Set("User-Agent", "private-agent-fingerprint")
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	request.Header.Set("baggage", "account=private-baggage-value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("handler status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if seenPath != "/private/server-secret" || seenQuery != "access_token=private-server-query" || seenHost != "customer-private.example" || seenRemoteAddr != "203.0.113.9:4321" || seenUserAgent != "private-agent-fingerprint" {
		t.Fatalf("application did not receive original request: path=%q query=%q host=%q remote=%q user-agent=%q", seenPath, seenQuery, seenHost, seenRemoteAddr, seenUserAgent)
	}
	if outboundURL != "https://private-target.example/internal/client-secret?api_key=private-query-secret" {
		t.Fatalf("upstream request target changed: %q", outboundURL)
	}
	if !strings.HasPrefix(outboundTraceparent, "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("outbound traceparent did not preserve trace ID: %q", outboundTraceparent)
	}
	if outboundBaggage != "account=private-baggage-value" {
		t.Fatalf("outbound baggage = %q, want preserved W3C baggage", outboundBaggage)
	}
	if outboundUserAgent != "private-client-fingerprint" {
		t.Fatalf("upstream User-Agent = %q, want original value preserved", outboundUserAgent)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("flush OTLP traces: %v", err)
	}
	stopped = true

	var exported exportRequest
	select {
	case exported = <-received:
	case <-ctx.Done():
		t.Fatal("collector did not receive an OTLP trace request")
	}
	if exported.path != "/v1/traces" {
		t.Fatalf("OTLP path = %q, want /v1/traces", exported.path)
	}
	if !strings.HasPrefix(exported.contentType, "application/x-protobuf") {
		t.Fatalf("OTLP content type = %q, want application/x-protobuf", exported.contentType)
	}

	var batch collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(exported.body, &batch); err != nil {
		t.Fatalf("decode OTLP protobuf: %v", err)
	}
	wantTraceID, _ := hex.DecodeString("4bf92f3577b34da6a3ce929d0e0e4736")
	foundTrace, foundService, foundServer, foundClient := false, false, false, false
	for _, resourceSpans := range batch.ResourceSpans {
		if resourceSpans.Resource != nil {
			for _, attribute := range resourceSpans.Resource.Attributes {
				if attribute.Key == "service.name" && attribute.Value.GetStringValue() == "porter-test" {
					foundService = true
				}
			}
		}
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				if bytes.Equal(span.TraceId, wantTraceID) {
					foundTrace = true
				}
				if span.Kind == tracepb.Span_SPAN_KIND_SERVER {
					foundServer = true
				}
				if span.Kind == tracepb.Span_SPAN_KIND_CLIENT {
					foundClient = true
				}
				for _, attribute := range span.Attributes {
					value := attribute.Value.GetStringValue()
					for _, sensitive := range []string{
						"server-secret", "private-server-query", "client-secret", "private-query-secret",
						"private-target.example", "customer-private.example", "private-agent-fingerprint", "private-client-fingerprint",
						"203.0.113.9", "private-baggage-value",
					} {
						if strings.Contains(value, sensitive) {
							t.Errorf("span attribute %q leaked sensitive value %q", attribute.Key, sensitive)
						}
					}
					switch attribute.Key {
					case "url.path":
						if value != "/" {
							t.Errorf("url.path = %q, want normalized /", value)
						}
					case "url.full":
						if value != "https://upstream/" {
							t.Errorf("url.full = %q, want normalized https://upstream/", value)
						}
					case "useragent.original", "client.address":
						t.Errorf("span contains sensitive high-cardinality attribute %q", attribute.Key)
					}
				}
			}
		}
	}
	if !foundTrace {
		t.Fatal("exported spans did not preserve the incoming W3C trace ID")
	}
	if !foundService {
		t.Fatal("exported resource did not include service.name=porter-test")
	}
	if !foundServer || !foundClient {
		t.Fatalf("expected server and client spans; found server=%v client=%v", foundServer, foundClient)
	}
}
