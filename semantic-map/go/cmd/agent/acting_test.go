package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
)

func actingMux(t *testing.T) *http.ServeMux {
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
	mux := http.NewServeMux()
	registerActingRoutes(mux, rt, pol)
	return mux
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
	if c := get(mux, "/policy").Code; c != http.StatusBadRequest {
		t.Errorf("missing subject: %d, want 400", c)
	}
	if c := get(mux, "/policy?subject=pod:nope").Code; c != http.StatusNotFound {
		t.Errorf("unknown subject: %d, want 404", c)
	}
}
