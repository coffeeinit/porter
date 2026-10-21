package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type stubRunner struct {
	url string
	err error
}

func (s stubRunner) Run(_ context.Context, _ string, _ io.Writer) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.url, nil
}

func TestShareOpenClose(t *testing.T) {
	r := NewRegistry(stubRunner{url: "https://abc.trycloudflare.com"})
	s, err := r.Open(context.Background(), "s1", "localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.URL, ".trycloudflare.com") {
		t.Fatal("bad url")
	}
	again, err := r.Open(context.Background(), "s2", "localhost:8080")
	if err != nil || again.URL != s.URL {
		t.Fatal("same local must reuse share")
	}
	if len(r.List()) != 1 {
		t.Fatal("one share expected")
	}
	r.Close("s1")
	r.Close("missing") // idempotent
	if len(r.List()) != 0 {
		t.Fatal("must be empty after close")
	}
}

func TestShareNoBinary(t *testing.T) {
	r := NewRegistry(stubRunner{err: errors.New("nope")})
	if _, err := r.Open(context.Background(), "s", "localhost:1"); err == nil {
		t.Fatal("runner failure must surface")
	}
}

func TestQuickURLRegex(t *testing.T) {
	if m := quickURL.FindString(".. https://my-app-123.trycloudflare.com .."); m == "" {
		t.Fatal("must parse quick url")
	}
}
