package judge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// canned server returns fixed typed answers regardless of the request.
func canned(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		var req wireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if req.Model == "" || len(req.Questions) == 0 {
			http.Error(w, "missing model/questions", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test","answers":{
			"c":{"type":"choice","choice":"b","probabilities":{"a":0.2,"b":0.8},"confidence":0.7},
			"n":{"type":"noul","noul":0.9},
			"s":{"type":"score","score":1.8,"legend":{"0":"Lo","1":"Mid","2":"Hi"},"probabilities":{"1":0.2,"2":0.8},"confidence":0.75}
		}}`))
	}))
}

func TestEvaluateRoundTrip(t *testing.T) {
	srv := canned(t)
	defer srv.Close()
	c := &Client{Key: "k", Endpoint: srv.URL, HTTP: srv.Client()}
	ans, err := c.Evaluate(context.Background(), "state",
		map[string]ChoiceQ{"c": {Instructions: "pick", Criteria: map[string]string{"a": "A", "b": "B"}}},
		map[string]NoulQ{"n": {Instructions: "yes?", TrueMeans: "y", FalseMeans: "n"}},
		map[string]ScoreQ{"s": {Instructions: "rate", Levels: []string{"Lo", "Mid", "Hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Choices["c"].Choice != "b" || ans.Nouls["n"] != 0.9 || ans.Scores["s"].Value != 1.8 {
		t.Fatalf("bad decode: %+v", ans)
	}
}

func TestMissingKeyErrors(t *testing.T) {
	if _, err := (&Client{}).Evaluate(context.Background(), "s", nil, nil, map[string]ScoreQ{
		"x": {Instructions: "r", Levels: []string{"A", "B"}},
	}); err == nil {
		t.Fatal("missing key must error, never call out")
	}
}

func TestNoQuestionsErrors(t *testing.T) {
	c := &Client{Key: "k"}
	if _, err := c.Evaluate(context.Background(), "s", nil, nil, nil); err == nil {
		t.Fatal("empty questions must error")
	}
}
