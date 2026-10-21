package observability

import (
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
)

// InitSentry enables Sentry only when both the explicit switch and DSN exist.
// The event hook removes request/user/breadcrumb data so operational secrets
// cannot be sent accidentally by the HTTP integration.
func InitSentry(enabled bool, dsn, environment, release string) (func(), error) {
	if !enabled || dsn == "" {
		return func() {}, nil
	}
	if environment == "" {
		environment = "development"
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      environment,
		Release:          release,
		SendDefaultPII:   false,
		AttachStacktrace: true,
		MaxBreadcrumbs:   20,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			event.Request = nil
			event.User = sentry.User{}
			event.Breadcrumbs = nil
			return event
		},
	}); err != nil {
		return nil, err
	}
	return func() { sentry.Flush(2 * time.Second) }, nil
}

// SentryHandler captures panics and HTTP failures through the official
// net/http integration. It is never installed when Sentry is disabled.
func SentryHandler(next http.Handler) http.Handler {
	return sentryhttp.New(sentryhttp.Options{Repanic: true}).Handle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &sentryResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if rw.status < http.StatusInternalServerError {
			return
		}
		if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("porter.method", r.Method)
				scope.SetTag("porter.status", http.StatusText(rw.status))
				if requestID := r.Header.Get("X-Request-Id"); requestID != "" {
					scope.SetTag("porter.request_id", requestID)
				}
				hub.CaptureMessage("Porter HTTP 5xx response")
			})
		}
	}))
}

type sentryResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *sentryResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
