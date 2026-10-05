// Package judge is the single plumbing for TypeSafe System One judgments
// (skill: typesafe-ai). Every Porter feature needing programmable common
// sense builds typed questions here; code in the owning package composes the
// answers into decisions. Rules, calculations, and execution stay in code.
//
// One call asks ALL independent questions together (they run in parallel and
// cannot see each other). A second call is warranted only when an earlier
// answer determines new evidence or options.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Endpoint for TypeSafe System One evaluations (live docs: docs.typesafe.ai/api).
const Endpoint = "https://api.typesafe.ai/v1/systemone"

// Model is the flagship System One model per current docs.
const Model = "jev-latest"

// ChoiceQ asks for one of a defined set. Always include a no-match option
// when nothing may fit; the model cannot choose an omitted value.
type ChoiceQ struct {
	Instructions string
	Criteria     map[string]string
}

// NoulQ asks whether a condition holds (probability of yes).
type NoulQ struct {
	Instructions string
	TrueMeans    string
	FalseMeans   string
}

// ScoreQ rates along ordered levels (each level self-contained).
type ScoreQ struct {
	Instructions string
	Levels       []string
}

// ChoiceA is one choice answer with its distribution.
type ChoiceA struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

// ScoreA is one score answer (probability-weighted position + legend).
type ScoreA struct {
	Value         float64
	Legend        map[string]string
	Probabilities map[string]float64
	Confidence    float64
}

// Answers holds every answer keyed by the question ids the caller chose.
type Answers struct {
	Choices map[string]ChoiceA
	Nouls   map[string]float64
	Scores  map[string]ScoreA
}

// Client performs evaluations. Key stays server-side (env/secret at call
// sites); it is never logged. HTTP is injectable for httptest stubs.
type Client struct {
	Key      string
	Model    string
	Endpoint string
	HTTP     *http.Client
}

func (c *Client) model() string {
	if c.Model != "" {
		return c.Model
	}
	return Model
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return Endpoint
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireRequest struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

// Evaluate asks all questions in ONE request and decodes typed answers.
// Callers compose behavior from probabilities with thresholds evaluated on
// their own data; typed output guarantees the interface, not truth.
func (c *Client) Evaluate(ctx context.Context, state any, choices map[string]ChoiceQ, nouls map[string]NoulQ, scores map[string]ScoreQ) (Answers, error) {
	if c.Key == "" {
		return Answers{}, fmt.Errorf("judge: missing API key; live judgments unavailable")
	}
	wire := map[string]wireQuestion{}
	for id, q := range choices {
		crit := map[string]any{}
		for opt, rubric := range q.Criteria {
			crit[opt] = rubric
		}
		wire[id] = wireQuestion{Type: "choice", Instructions: q.Instructions, Criteria: crit}
	}
	for id, q := range nouls {
		wire[id] = wireQuestion{Type: "noul", Instructions: q.Instructions, Criteria: map[string]any{
			"true": q.TrueMeans, "false": q.FalseMeans,
		}}
	}
	for id, q := range scores {
		levels := make([]any, 0, len(q.Levels))
		for _, l := range q.Levels {
			levels = append(levels, l)
		}
		wire[id] = wireQuestion{Type: "score", Instructions: q.Instructions, Criteria: levels}
	}
	if len(wire) == 0 {
		return Answers{}, fmt.Errorf("judge: no questions to ask")
	}
	payload, err := json.Marshal(wireRequest{State: state, Model: c.model(), Questions: wire})
	if err != nil {
		return Answers{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return Answers{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.http().Do(req)
	if err != nil {
		return Answers{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Answers{}, fmt.Errorf("judge: typesafe HTTP %s", resp.Status)
	}
	var body struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Answers{}, err
	}
	out := Answers{Choices: map[string]ChoiceA{}, Nouls: map[string]float64{}, Scores: map[string]ScoreA{}}
	for id := range choices {
		var a struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		}
		if err := json.Unmarshal(body.Answers[id], &a); err != nil {
			return Answers{}, fmt.Errorf("judge: decode choice %s: %w", id, err)
		}
		out.Choices[id] = ChoiceA{Choice: a.Choice, Probabilities: a.Probabilities, Confidence: a.Confidence}
	}
	for id := range nouls {
		var a struct {
			Value float64 `json:"noul"`
		}
		if err := json.Unmarshal(body.Answers[id], &a); err != nil {
			return Answers{}, fmt.Errorf("judge: decode noul %s: %w", id, err)
		}
		out.Nouls[id] = a.Value
	}
	for id := range scores {
		var a struct {
			Value         float64            `json:"score"`
			Legend        map[string]string  `json:"legend"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		}
		if err := json.Unmarshal(body.Answers[id], &a); err != nil {
			return Answers{}, fmt.Errorf("judge: decode score %s: %w", id, err)
		}
		out.Scores[id] = ScoreA{Value: a.Value, Legend: a.Legend, Probabilities: a.Probabilities, Confidence: a.Confidence}
	}
	return out, nil
}
