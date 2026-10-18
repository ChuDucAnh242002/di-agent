package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/relief"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

func actingMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _, _ := actingMuxWith(t, false)
	return mux
}

// actingMuxWith is the acting routes over a dry-run runtime, with a relief loop
// when withLoop is set.
func actingMuxWith(t *testing.T, withLoop bool) (*http.ServeMux, *relief.Loop, *runtime.DryRun) {
	t.Helper()
	s := spec.Default()
	rt, err := runtime.Build("dry-run", runtime.Env{NodeID: "n1", Spec: s.Runtime})
	if err != nil {
		t.Fatal(err)
	}
	dr := rt.(*runtime.DryRun)
	dr.Put(runtime.Workload{Subject: "pod:a", Name: "a",
		Labels: map[string]string{"di-agent/freeze": "true"},
		Can:    map[string]string{runtime.ActionCPULimit: ""}})
	dr.Put(runtime.Workload{Subject: "pod:b", Name: "b",
		Can: map[string]string{runtime.ActionCPULimit: ""}})
	pol, err := policy.New(s.Policy)
	if err != nil {
		t.Fatal(err)
	}
	var loop *relief.Loop
	if withLoop {
		loop, err = relief.New(relief.Config{Relief: s.Loops.Relief, NodeID: "n1", Runtime: rt, Policy: pol,
			Map: statemap.New(statemap.Config{Owner: "n1"}, nil)})
		if err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	registerActingRoutes(mux, rt, pol, loop)
	return mux, loop, dr
}

func post(mux *http.ServeMux, url string, json bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, url, nil)
	if json {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func get(mux *http.ServeMux, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestWorkloadsCarryTheirPolicy(t *testing.T) {
	rec := get(actingMux(t), "/workloads")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got []struct {
		Subject string              `json:"subject"`
		Labels  map[string]string   `json:"labels"`
		CPU     map[string]*float64 `json:"cpu"`
		Can     map[string]string   `json:"can"`
		Policy  struct {
			Frozen bool `json:"frozen"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workloads, want 2: %s", len(got), rec.Body)
	}
	frozen := map[string]bool{}
	for _, w := range got {
		frozen[w.Subject] = w.Policy.Frozen
	}
	for _, w := range got {
		if w.Labels == nil || w.Can == nil || len(w.CPU) != 2 {
			t.Errorf("%s: labels/can must be objects and cpu {request, limit}: %+v", w.Subject, w)
		}
		if w.Subject == "pod:a" && w.Labels["di-agent/freeze"] != "true" {
			t.Errorf("pod:a labels = %v", w.Labels)
		}
		if _, ok := w.Can[runtime.ActionCPULimit]; !ok {
			t.Errorf("%s: can = %v, want cpu.limit", w.Subject, w.Can)
		}
	}
	if !frozen["pod:a"] || frozen["pod:b"] {
		t.Errorf("frozen = %v, want pod:a frozen and pod:b not", frozen)
	}

	// The settings a policy carries are snake_case like the rest of the API, not Go field names.
	var raw []struct {
		Policy struct {
			Settings map[string]any `json:"settings"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allow", "deny", "floor", "max_actions_per_hour", "cooldown"} {
		if _, ok := raw[0].Policy.Settings[key]; !ok {
			t.Errorf("policy.settings has no %q: %v", key, raw[0].Policy.Settings)
		}
	}
	// A duration reads as it does in agent.yaml and /config, not as nanoseconds.
	if got := raw[0].Policy.Settings["cooldown"]; got != "10m0s" {
		t.Errorf("policy.settings.cooldown = %v (%T), want \"10m0s\"", got, got)
	}
}

func TestPolicyRouteExplainsOne(t *testing.T) {
	mux := actingMux(t)
	rec := get(mux, "/policy?subject=pod:b")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var eff policy.Effective
	if err := json.Unmarshal(rec.Body.Bytes(), &eff); err != nil {
		t.Fatal(err)
	}
	if eff.Rule != -1 || eff.Frozen {
		t.Errorf("pod:b = rule %d frozen %v, want -1 and false", eff.Rule, eff.Frozen)
	}
	if eff.Settings.Cooldown != 10*time.Minute {
		t.Errorf("cooldown read back as %s, want 10m", eff.Settings.Cooldown)
	}
	if c := get(mux, "/policy").Code; c != http.StatusBadRequest {
		t.Errorf("missing subject: %d, want 400", c)
	}
	if c := get(mux, "/policy?subject=pod:nope").Code; c != http.StatusNotFound {
		t.Errorf("unknown subject: %d, want 404", c)
	}
}

func TestReliefRoutesNeedTheLoop(t *testing.T) {
	mux := actingMux(t)
	for _, r := range []*httptest.ResponseRecorder{
		get(mux, "/relief"), get(mux, "/relief/actions"),
		post(mux, "/relief/pause", true), post(mux, "/relief/resume", true),
		post(mux, "/relief/actions/x/revert", true),
	} {
		if r.Code != http.StatusNotFound || !strings.Contains(r.Body.String(), "relief is not enabled in the spec") {
			t.Errorf("status %d body %s, want 404 naming the spec", r.Code, r.Body)
		}
	}
}

func TestPauseAndResume(t *testing.T) {
	mux, _, _ := actingMuxWith(t, true)
	paused := func() bool {
		var st struct {
			Paused bool `json:"paused"`
		}
		rec := get(mux, "/relief")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /relief = %d: %s", rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st.Paused
	}
	if paused() {
		t.Fatal("a new loop is paused")
	}
	if rec := post(mux, "/relief/pause", true); rec.Code != http.StatusOK || !paused() {
		t.Fatalf("pause: %d, paused=%v", rec.Code, paused())
	}
	if rec := post(mux, "/relief/resume", true); rec.Code != http.StatusOK || paused() {
		t.Fatalf("resume: %d, paused=%v", rec.Code, paused())
	}
}

func TestManualRevert(t *testing.T) {
	mux, loop, dr := actingMuxWith(t, true)
	limit := 0.5
	dr.Put(runtime.Workload{Subject: "pod:c", Name: "c", CPU: runtime.Resource{Limit: &limit},
		Can: map[string]string{runtime.ActionCPULimit: ""}})
	dr.PutUndo(runtime.Undo{Runtime: "dry-run", NodeID: "n1", Subject: "pod:c", Action: runtime.ActionCPULimit,
		Original: runtime.Resource{Limit: &limit}, Applied: 0.25, DecisionID: "act-1",
		Expires: time.Now().Add(time.Hour)})
	if err := loop.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var acts []relief.Action
	if err := json.Unmarshal(get(mux, "/relief/actions").Body.Bytes(), &acts); err != nil || len(acts) != 1 {
		t.Fatalf("actions = %v, err %v", acts, err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(get(mux, "/relief/actions").Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw[0]["undo"].(map[string]any)["node_id"]; !ok {
		t.Errorf("undo keys = %v, want snake_case node_id", raw[0]["undo"])
	}
	if rec := post(mux, "/relief/actions/nope/revert", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", rec.Code)
	}
	if rec := post(mux, "/relief/actions/"+acts[0].ID+"/revert", true); rec.Code != http.StatusOK {
		t.Fatalf("revert = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(get(mux, "/relief/actions").Body.Bytes(), &acts); err != nil || acts[0].Outcome != relief.OutcomeManual {
		t.Fatalf("outcome = %q, want manual (%v)", acts[0].Outcome, err)
	}
	if len(dr.Applied()) != 0 {
		t.Error("the runtime still holds the change")
	}
}

func TestMutatingReliefRoutesNeedJSON(t *testing.T) {
	mux, _, _ := actingMuxWith(t, true)
	for _, u := range []string{"/relief/pause", "/relief/resume", "/relief/actions/x/revert"} {
		// The same refusal as every other mutating route: requireJSON, 400.
		if rec := post(mux, u, false); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s without a JSON content type = %d, want 400", u, rec.Code)
		}
	}
}

func TestOnlyCPUConsumersAreWorkloads(t *testing.T) {
	m := statemap.New(statemap.Config{Owner: "n1", AdmitUnknown: true}, nil)
	unit := [2]float64{0, 1}
	for _, o := range []statemap.Observation{
		{ID: "cpu_utilization@pod:a", Subject: "pod:a", Value: 0.2},
		{ID: "cpu.cpu.user@cpu:cpu0", Subject: "cpu:cpu0", Value: 0.3},
		{ID: "disk.io.reads@disk:sda", Subject: "disk:sda", Value: 0.1},
	} {
		o.At, o.Range = time.Now(), &unit
		if err := m.Record(o); err != nil {
			t.Fatal(err)
		}
	}
	got := workloadSubjects(m)()
	if len(got) != 1 || got[0] != "pod:a" {
		t.Errorf("workload subjects = %v, want [pod:a]", got)
	}
}
