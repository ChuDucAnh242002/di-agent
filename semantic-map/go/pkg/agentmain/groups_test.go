package agentmain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/groups"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

// TestTheNodesGroupsAreInTheMapAndOnTheWire: one tick of the groups loop declares each
// group whose members use the machine as a group: subject of the map, serves the whole
// topology at GET /groups, and retires a group nothing belongs to any more, so its sum
// stops speaking for workloads that left it.
func TestTheNodesGroupsAreInTheMapAndOnTheWire(t *testing.T) {
	st := newTestState(t)
	rng := [2]float64{0, 1}
	for _, o := range []statemap.Observation{
		{ID: "cpu_utilization@pod:pg", Subject: "pod:pg", Value: 0.2},
		{ID: "cpu_utilization@pod:redis", Subject: "pod:redis", Value: 0.1},
		{ID: "cpu_utilization@unit:ollama.service", Subject: "unit:ollama.service", Value: 0.3},
		{ID: "net.net.sent@net:enp0s1", Subject: "net:enp0s1", Value: 5},
	} {
		o.At, o.Range = time.Now(), &rng
		if err := st.Record(o); err != nil {
			t.Fatal(err)
		}
	}
	ws := []runtime.Workload{
		{Subject: "pod:pg", Labels: map[string]string{"di-agent/group": "storage"}},
		{Subject: "pod:redis", Labels: map[string]string{"di-agent/group": "storage"}},
	}
	g := &grouper{state: st, cfg: groups.DefaultConfig(), linux: groups.Linux{},
		workloads: func(context.Context) ([]runtime.Workload, error) { return ws, nil }, logf: t.Logf}
	g.tick(context.Background())

	p, ok := st.Property("cpu_utilization@group:storage")
	if !ok || p.Value < 0.299 || p.Value > 0.301 {
		t.Fatalf("storage CPU %+v; want the group in the map at 0.3", p)
	}
	if _, ok := st.Property("cpu_utilization@group:system"); !ok {
		t.Fatal("no system group for the modelled unit")
	}
	if _, ok := st.Property("cpu_utilization@group:hardware"); ok {
		t.Fatal("the hardware group is in the map; its members use nothing of their own")
	}

	mux := http.NewServeMux()
	registerGroupsRoute(mux, g)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/groups", nil))
	var top groups.Topology
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil || len(top.Groups) != 3 {
		t.Fatalf("GET /groups %d %s; want storage, system and hardware", rec.Code, rec.Body)
	}

	ws = nil // the label is gone from both
	g.tick(context.Background())
	if p, _ := st.Property("cpu_utilization@group:storage"); p.Status != statemap.Retired {
		t.Fatalf("storage %v; want it retired once nothing belongs to it", p.Status)
	}
}
