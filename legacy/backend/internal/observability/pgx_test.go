package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestSQLOperationDoesNotReturnSQLText(t *testing.T) {
	for _, test := range []struct {
		sql  string
		want string
	}{
		{sql: "SELECT password FROM users WHERE id = $1", want: "SELECT"},
		{sql: "  insert into projects(name) values ($1)", want: "INSERT"},
		{sql: "", want: "UNKNOWN"},
	} {
		if got := sqlOperation(test.sql); got != test.want {
			t.Fatalf("sqlOperation(%q) = %q, want %q", test.sql, got, test.want)
		}
		if got := sqlOperation(test.sql); got == test.sql {
			t.Fatalf("sql text leaked from %q", test.sql)
		}
	}
}

func TestDisabledTracingIsNoop(t *testing.T) {
	shutdown, err := InitTracing(context.Background(), false, "porter-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if span := trace.SpanFromContext(context.Background()); span.IsRecording() {
		t.Fatal("disabled tracing returned a recording span")
	}
}
