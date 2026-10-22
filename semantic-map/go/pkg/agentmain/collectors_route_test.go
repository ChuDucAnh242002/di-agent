package agentmain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/collectors"
	"github.com/DiyazY/di-agent/pkg/types"
)

// fakeCollector is a member that never needs to be called: the route reports
// status, it does not collect.
type fakeCollector struct{}

func (fakeCollector) Collect() ([]*types.MetricSample, error) { return nil, nil }
func (fakeCollector) SourceID() string                        { return "fake" }
func (fakeCollector) AvailableMetrics() []types.MetricType    { return nil }

func getCollectors(t *testing.T, sup *collectors.Supervisor) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	registerCollectorsRoute(mux, sup, 10*time.Second)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/collectors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, rec.Body)
	}
	if got["interval_s"] != float64(10) {
		t.Errorf("interval_s = %v, want 10", got["interval_s"])
	}
	return got
}

// TestCollectorsRouteLists pins that GET /collectors reports each member under the
// field names the spec gives.
func TestCollectorsRouteLists(t *testing.T) {
	sup := collectors.NewSupervisor("n1", []collectors.Member{
		{Name: "probe", Kind: "type", Type: "fake", C: fakeCollector{}, Timeout: time.Second},
	}, nil)
	got := getCollectors(t, sup)
	list, ok := got["collectors"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("collectors = %v, want one entry", got["collectors"])
	}
	m := list[0].(map[string]any)
	if m["name"] != "probe" || m["kind"] != "type" || m["state"] != "starting" {
		t.Errorf("collectors[0] = %v, want name probe, kind type, state starting", m)
	}
}

// TestCollectorsRouteNil pins that an agent with no collectors answers with an
// empty list rather than null, so a client can range over it unconditionally.
func TestCollectorsRouteNil(t *testing.T) {
	mux := http.NewServeMux()
	registerCollectorsRoute(mux, nil, 10*time.Second)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/collectors", nil))
	if !strings.Contains(rec.Body.String(), `"collectors":[]`) {
		t.Fatalf("body = %s, want \"collectors\":[]", rec.Body)
	}
	getCollectors(t, nil)
}
