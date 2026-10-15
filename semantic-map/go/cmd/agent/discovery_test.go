package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/explain"
	"github.com/DiyazY/di-agent/pkg/profiles"
	"github.com/DiyazY/di-agent/pkg/statemap"
	"github.com/DiyazY/di-agent/pkg/types"
)

// TestNodePairDiscoveredAndConfirmedWithADirection: two node-level properties that
// move together become one undirected candidate. Confirming it without saying which
// end it runs from is refused with 400 and leaves it pending; confirming with "from"
// declares a Discovered relationship in the state model running that way.
func TestNodePairDiscoveredAndConfirmedWithADirection(t *testing.T) {
	state := newTestState(t)
	sm, _, err := profiles.Build("edge-minimal", profiles.Config{
		StateMap: state, EMAAlpha: 0.2, ConvergenceThreshold: 500, MinTrustScore: 0.5,
		UseRuleBasedTuner: true, UseProposer: true, ProposerThreshold: 0.8, ProposerMinPairs: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerRoutes(mux, sm, explain.NewDisabled())
	registerStateRoutes(mux, state)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rng := [2]float64{0, 1}
	t0 := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 30; i++ {
		x := float64(i%10) / 10
		at := t0.Add(time.Duration(i) * 5 * time.Second).Unix()
		for _, s := range []*types.MetricSample{
			{MetricType: types.CPUUtilization, Value: 0.1 + 0.8*x},
			{MetricType: types.CPUPressureRatio, Value: 0.05 + 0.4*x},
		} {
			s.Unit, s.Range, s.TimestampUnix = "fraction", &rng, at
			s.EventID = string(s.MetricType) + time.Unix(at, 0).Format(time.RFC3339)
			if err := sm.IngestSample(s); err != nil {
				t.Fatalf("ingest %s: %v", s.MetricType, err)
			}
		}
	}

	const id = "cpu_pressure_ratio<->cpu_utilization"
	cands, _ := sm.PendingCandidates()
	var found bool
	for _, c := range cands {
		if c.CandidateID == id && c.Undirected {
			found = true
		}
	}
	if !found {
		t.Fatalf("no undirected %s among %d pending candidates", id, len(cands))
	}

	confirm := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/candidates/"+url.PathEscape(id)+"/confirm", bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := confirm(""); code != http.StatusBadRequest {
		t.Errorf("confirm without a direction: %d; want 400", code)
	}
	if code := confirm(`{"from":"memory_utilization"}`); code != http.StatusBadRequest {
		t.Errorf("confirm from a property that is not an end: %d; want 400", code)
	}
	if code := confirm(`{"from":"cpu_utilization"}`); code != http.StatusNoContent {
		t.Fatalf("confirm from cpu_utilization: %d; want 204", code)
	}

	var v statemap.StateView
	if code := getState(t, srv.URL+"/state", &v); code != 200 {
		t.Fatalf("GET /state: %d", code)
	}
	for _, r := range v.Relationships {
		if r.From == "cpu_utilization" && r.To == "cpu_pressure_ratio" {
			if r.Provenance != statemap.Discovered {
				t.Errorf("provenance %q; want discovered", r.Provenance)
			}
			b, _ := json.Marshal(r)
			t.Logf("declared: %s", b)
			return
		}
	}
	t.Error("no cpu_utilization -> cpu_pressure_ratio relationship after the confirm")
}
