package notify

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookSend(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := WebhookSender{URL: srv.URL + "/hook", Kind: "slack"}
	if err := s.Send([]string{"#ops"}, "deploy", "ok"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/hook" {
		t.Fatal("wrong path")
	}
	bad := WebhookSender{URL: srv.URL, Kind: "telegram"}
	if err := bad.Send([]string{"1"}, "x", "y"); err == nil {
		t.Fatal("telegram without token must fail")
	}
	if err := (WebhookSender{Kind: "slack"}).Send([]string{"c"}, "s", "b"); err == nil {
		t.Fatal("missing URL must fail")
	}
}
