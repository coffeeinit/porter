package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type serverRequestKey struct{}
type clientRequestKey struct{}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// HTTPHandler creates an HTTP server span using a sanitized request view. The
// wrapped application receives the original URL, host, headers and peer address.
func HTTPHandler(next http.Handler, operation string) http.Handler {
	instrumented := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original, ok := r.Context().Value(serverRequestKey{}).(*http.Request)
		if !ok {
			http.Error(w, "missing original request", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, original.WithContext(r.Context()))
	}), operation)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safe := r.Clone(context.WithValue(r.Context(), serverRequestKey{}, r))
		if r.URL != nil {
			safeURL := *r.URL
			safeURL.User = nil
			safeURL.Host = "porter"
			safeURL.Path = "/"
			safeURL.RawPath = ""
			safeURL.RawQuery = ""
			safeURL.Fragment = ""
			safeURL.RawFragment = ""
			safeURL.Opaque = ""
			safe.URL = &safeURL
		}
		safe.Host = "porter"
		safe.RemoteAddr = ""
		safe.RequestURI = "/"
		safe.Header = r.Header.Clone()
		safe.Header.Del("User-Agent")
		instrumented.ServeHTTP(w, safe)
	})
}

// HTTPTransport creates a traced RoundTripper whose span uses a normalized
// target URL. It restores the original target immediately before the underlying
// transport, retaining traceparent injection without exporting query/path data.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	instrumented := otelhttp.NewTransport(roundTripperFunc(func(safe *http.Request) (*http.Response, error) {
		original, ok := safe.Context().Value(clientRequestKey{}).(*http.Request)
		if !ok || original.URL == nil {
			return nil, fmt.Errorf("missing original outbound request")
		}
		actual := safe.Clone(safe.Context())
		actualURL := *original.URL
		actual.URL = &actualURL
		actual.Host = original.Host
		actual.RequestURI = original.RequestURI
		actual.Header = safe.Header.Clone()
		if userAgent := original.Header.Get("User-Agent"); userAgent != "" {
			actual.Header.Set("User-Agent", userAgent)
		} else {
			actual.Header.Del("User-Agent")
		}
		return base.RoundTrip(actual)
	}))

	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request == nil || request.URL == nil {
			return nil, fmt.Errorf("outbound request URL is required")
		}
		safe := request.Clone(context.WithValue(request.Context(), clientRequestKey{}, request))
		safe.Header = request.Header.Clone()
		safe.Header.Del("User-Agent")
		safeURL := *request.URL
		safeURL.User = nil
		safeURL.Host = "upstream"
		safeURL.Path = "/"
		safeURL.RawPath = ""
		safeURL.RawQuery = ""
		safeURL.Fragment = ""
		safeURL.RawFragment = ""
		safeURL.Opaque = ""
		safe.URL = &safeURL
		safe.Host = "upstream"
		safe.RequestURI = ""
		return instrumented.RoundTrip(safe)
	})
}
