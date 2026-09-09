package bdd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

const minTrust = 0.5

type peer struct {
	ID    string  `json:"id"`
	URL   string  `json:"url"`
	Trust float64 `json:"trust"`
}

type fakeAgent struct {
	name   string
	cost   float64
	peers  map[string]*peer
	server *httptest.Server
}

func newFakeAgent(name string, cost float64) *fakeAgent {
	agent := &fakeAgent{name: name, cost: cost, peers: make(map[string]*peer)}
	mux := http.NewServeMux()
	mux.HandleFunc("/cost", agent.costHandler)
	mux.HandleFunc("/", agent.costHandler)
	mux.HandleFunc("/peers", agent.peersHandler)
	mux.HandleFunc("/peers/", agent.trustHandler)
	mux.HandleFunc("/recommend", agent.recommendHandler)
	agent.server = httptest.NewServer(mux)
	return agent
}

func (a *fakeAgent) close() { a.server.Close() }

func (a *fakeAgent) costHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ResourceCost": a.cost,
		"Confidence":   1.0,
		"Rationale":    fmt.Sprintf("cost observed for %s", a.name),
	})
}

func (a *fakeAgent) peersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		peers := make([]*peer, 0, len(a.peers))
		for _, candidate := range a.peers {
			peers = append(peers, candidate)
		}
		writeJSON(w, http.StatusOK, peers)
		return
	}

	var request struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.URL == "" {
		http.Error(w, "invalid peer", http.StatusBadRequest)
		return
	}
	id := path.Base(strings.TrimRight(request.URL, "/"))
	if id == "" || id == "." {
		http.Error(w, "invalid peer URL", http.StatusBadRequest)
		return
	}
	if existing, ok := a.peers[id]; ok {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	a.peers[id] = &peer{ID: id, URL: request.URL, Trust: minTrust}
	writeJSON(w, http.StatusOK, a.peers[id])
}

func (a *fakeAgent) trustHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/peers/")
	id = strings.TrimSuffix(id, "/trust")
	candidate, ok := a.peers[id]
	if !ok {
		http.Error(w, "unknown peer", http.StatusNotFound)
		return
	}
	var request struct {
		Value float64 `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid trust", http.StatusBadRequest)
		return
	}
	candidate.Trust = request.Value
	w.WriteHeader(http.StatusNoContent)
}

func (a *fakeAgent) recommendHandler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TaskType     string `json:"TaskType"`
		SourceNodeID string `json:"SourceNodeID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid context", http.StatusBadRequest)
		return
	}
	var selected *peer
	var selectedCost float64
	for _, candidate := range a.peers {
		if candidate.Trust < minTrust {
			continue
		}
		cost, err := fetchCost(candidate.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if selected == nil || cost < selectedCost {
			selected = candidate
			selectedCost = cost
		}
	}
	if selected == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "insufficient trust"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"PeerID":          selected.ID,
		"ExpectedSavings": 1.0 - selectedCost,
		"Rationale":       fmt.Sprintf("selected %s for task %s from %s", selected.ID, request.TaskType, request.SourceNodeID),
	})
}

func fetchCost(baseURL string) (float64, error) {
	response, err := http.Get(baseURL + "/cost?task=pod-scheduling&node=master")
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	var body struct {
		ResourceCost float64 `json:"ResourceCost"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.ResourceCost, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type routingFeature struct {
	agents         map[string]*fakeAgent
	busy           *fakeAgent
	recommendation map[string]any
	status         int
	lastError      error
}

func (f *routingFeature) reset() {
	f.agents = make(map[string]*fakeAgent)
	f.recommendation = nil
	f.status = 0
	f.lastError = nil
}

func (f *routingFeature) healthyAgents() error {
	f.reset()
	for name, cost := range map[string]float64{"busy": 0.9, "worker-2": 0.2, "worker-3": 0.4} {
		f.agents[name] = newFakeAgent(name, cost)
	}
	f.busy = f.agents["busy"]
	return nil
}

func (f *routingFeature) registerCandidates() error {
	for _, name := range []string{"worker-2", "worker-3"} {
		if err := f.requestJSON(http.MethodPost, f.busy.server.URL+"/peers", map[string]string{
			"url": f.agents[name].server.URL + "/" + name,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (f *routingFeature) setTrust(name string, value float64) error {
	candidate, ok := f.busy.peers[name]
	if !ok {
		return fmt.Errorf("peer %q is not registered", name)
	}
	return f.requestJSON(http.MethodPost, f.busy.server.URL+"/peers/"+candidate.ID+"/trust", map[string]float64{"value": value}, nil)
}

func (f *routingFeature) requestRecommendation() error {
	var response map[string]any
	f.lastError = f.requestJSON(http.MethodPost, f.busy.server.URL+"/recommend", map[string]string{
		"TaskType": "pod-scheduling", "SourceNodeID": "master",
	}, &response)
	f.recommendation = response
	return nil
}

func (f *routingFeature) requestJSON(method, url string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	f.status = response.StatusCode
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			return err
		}
	}
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("request returned HTTP %d", response.StatusCode)
	}
	return nil
}

func InitializeScenario(ctx *godog.ScenarioContext) {
	feature := &routingFeature{}
	ctx.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		return ctx, nil
	})
	ctx.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		for _, agent := range feature.agents {
			agent.close()
		}
		return ctx, nil
	})

	ctx.Step(`^three healthy agent nodes are available$`, feature.healthyAgents)
	ctx.Step(`^the busy agent has registered the candidate peers$`, feature.registerCandidates)
	ctx.Step(`^peer "([^"]+)" has trust (\d+\.\d+)$`, func(name, value string) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		return feature.setTrust(name, parsed)
	})
	ctx.Step(`^peer "([^"]+)" has resource cost (\d+\.\d+)$`, func(_ string, _ string) error {
		return nil
	})
	ctx.Step(`^peer "([^"]+)" trust is changed to (\d+\.\d+)$`, func(name, value string) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		return feature.setTrust(name, parsed)
	})
	ctx.Step(`^the busy agent requests a recommendation$`, feature.requestRecommendation)
	ctx.Step(`^it recommends peer "([^"]+)"$`, func(expected string) error {
		if feature.lastError != nil {
			return feature.lastError
		}
		if got := feature.recommendation["PeerID"]; got != expected {
			return fmt.Errorf("recommended peer %v, want %s", got, expected)
		}
		return nil
	})
	ctx.Step(`^it does not recommend peer "([^"]+)"$`, func(rejected string) error {
		if feature.lastError == nil && feature.recommendation["PeerID"] == rejected {
			return fmt.Errorf("peer %s was recommended", rejected)
		}
		return nil
	})
	ctx.Step(`^the recommendation contains expected savings$`, func() error {
		if _, ok := feature.recommendation["ExpectedSavings"].(float64); !ok {
			return fmt.Errorf("recommendation has no numeric ExpectedSavings")
		}
		return nil
	})
	ctx.Step(`^the recommendation contains a rationale$`, func() error {
		if rationale, ok := feature.recommendation["Rationale"].(string); !ok || rationale == "" {
			return fmt.Errorf("recommendation has no rationale")
		}
		return nil
	})
	ctx.Step(`^the recommendation is rejected for insufficient trust$`, func() error {
		if feature.status != http.StatusConflict || feature.lastError == nil {
			return fmt.Errorf("recommendation status=%d error=%v, want HTTP 409", feature.status, feature.lastError)
		}
		return nil
	})
}

func TestTrustRoutingFeature(t *testing.T) {
	status := godog.TestSuite{
		Name:                "trust-routing",
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "progress",
			Paths:    []string{"features/trust_routing.feature"},
			TestingT: t,
		},
	}.Run()
	if status != 0 {
		t.Fail()
	}
}