package observability

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type pgxSpanKey struct{}

// NewPGXTracer returns a pgx-native query tracer. It records the operation
// kind and outcome only; SQL text and arguments are deliberately excluded.
func NewPGXTracer() pgx.QueryTracer {
	return &pgxTracer{tracer: otel.Tracer("porter.database")}
}

type pgxTracer struct{ tracer trace.Tracer }

func (t *pgxTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := t.tracer.Start(ctx, "postgres.query", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", sqlOperation(data.SQL)),
	))
	return context.WithValue(ctx, pgxSpanKey{}, span)
}

func (t *pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(pgxSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, "postgres query failed")
	}
	span.End()
}

func sqlOperation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "UNKNOWN"
	}
	return strings.ToUpper(fields[0])
}
