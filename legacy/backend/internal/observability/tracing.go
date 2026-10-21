// Package observability contains low-level, opt-in telemetry boundaries.
package observability

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// InitTracing configures the global OpenTelemetry trace provider when enabled.
// OTLP endpoint, protocol, headers, timeout, and sampling are intentionally
// read from the standard OTEL_* environment variables by the SDK exporter.
// The returned shutdown function flushes spans during graceful shutdown.
func InitTracing(ctx context.Context, enabled bool, serviceName string) (func(context.Context) error, error) {
	if !enabled {
		return func(context.Context) error { return nil }, nil
	}
	if serviceName == "" {
		serviceName = "porter-control-plane"
	}
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", serviceName),
		attribute.String("service.namespace", "porter"),
	))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown, nil
}

// HTTPHandler instruments the full control-plane handler chain. Request IDs
// are attached to the active span by the existing structured-log middleware.
func HTTPHandler(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "porter.http")
}

// RequestIDAttribute attaches the existing Porter request ID to an active
// span. It is deliberately a separate helper so the logging package can keep
// its existing output format while adding trace correlation.
func RequestIDAttribute(ctx context.Context, requestID string) {
	if requestID == "" {
		return
	}
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(attribute.String("porter.request_id", requestID))
	}
}
