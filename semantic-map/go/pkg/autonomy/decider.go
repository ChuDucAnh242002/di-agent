package autonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DiyazY/di-agent/pkg/types"
)

// DecidePrompt versions the questions a decision model is asked. A change to them is
// a change to what every later decision means, so it is recorded in each.
const DecidePrompt = "review-decide-v1"

// The orientation question's options, mapped back to the candidate's ends.
const (
	orientFirst   = "first_drives_second"
	orientSecond  = "second_drives_first"
	orientUnclear = "unclear"
)

// SystemOne is a Decider over Ollama's decision endpoint (POST /v1/systemone): a
// state and a schema of typed questions in, a probability per option out, in one
// forward pass. Decision models such as nimble-v3 and tev1 serve only this endpoint.
type SystemOne struct {
	BaseURL string // e.g. http://localhost:11434
	Model   string
	HTTP    *http.Client
}

// NewSystemOne builds a decision-model client.
func NewSystemOne(baseURL, model string, timeout time.Duration) *SystemOne {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &SystemOne{BaseURL: strings.TrimRight(baseURL, "/"), Model: model, HTTP: &http.Client{Timeout: timeout}}
}

func (d *SystemOne) Name() string { return d.Model }

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type systemOneRequest struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]question `json:"questions"`
}

type systemOneResponse struct {
	Answers map[string]struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
	Error string `json:"error"`
}

// Decide asks whether the candidate should be confirmed, rejected or deferred and,
// when it is undirected, which end drives the other.
func (d *SystemOne) Decide(ctx context.Context, e Evidence, analysis string) (*Verdict, error) {
	c := e.Candidate
	qs := map[string]question{
		"action": {
			Type: "choice",
			Instructions: "Should this candidate be recorded in the machine's model as a relationship " +
				"between the two metrics?",
			Criteria: map[string]string{
				types.ReviewConfirm: "Record it: the source plausibly drives, feeds or accounts for the target on this " +
					"machine, and the evidence is strong and clean enough to rely on.",
				types.ReviewReject: "Do not record it: the correlation is most likely coincidence, both metrics " +
					"following a shared driver or overall load, or an artefact of how they are measured, so it says " +
					"nothing about how one affects the other.",
				types.ReviewDefer: "Not yet: it may be real, but the evidence is too thin or too mixed to decide " +
					"now; wait for more readings.",
			},
		},
	}
	if c.Undirected {
		qs["orientation"] = question{
			Type:         "choice",
			Instructions: "If one of the two drives the other, which one?",
			Criteria: map[string]string{
				orientFirst:   c.FromID + " drives " + c.ToID + ".",
				orientSecond:  c.ToID + " drives " + c.FromID + ".",
				orientUnclear: "Neither can be said to drive the other from this evidence.",
			},
		}
	}
	state := stateText(e, analysis)
	raw, err := json.Marshal(systemOneRequest{Model: d.Model, State: state, Questions: qs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/v1/systemone", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Model, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Model, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d: %s", d.Model, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out systemOneResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", d.Model, err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s: %s", d.Model, out.Error)
	}
	action, ok := out.Answers["action"]
	if !ok || len(action.Probabilities) == 0 {
		return nil, fmt.Errorf("%s answered no action probabilities", d.Model)
	}
	v := &Verdict{Model: d.Model, Prompt: DecidePrompt, State: state, Actions: action.Probabilities}
	if c.Undirected {
		o := out.Answers["orientation"].Probabilities
		v.Orientation = map[string]float64{
			c.FromID:      o[orientFirst],
			c.ToID:        o[orientSecond],
			orientUnclear: o[orientUnclear],
		}
	}
	return v, nil
}
