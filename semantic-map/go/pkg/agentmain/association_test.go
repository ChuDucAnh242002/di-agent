package agentmain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/explain"
	"github.com/DiyazY/di-agent/pkg/profiles"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

func TestAssociationMetadataSurvivesGraphWireProjection(t *testing.T) {
	state := newTestState(t)
	for _, id := range []string{"a", "b"} {
		if err := state.Record(statemap.Observation{ID: id, Value: 0.5, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.DeclareRelationship(statemap.Relationship{
		From: "b", To: "a", Sign: 1, Label: "association", Association: true, Provenance: statemap.Discovered,
	}); err != nil {
		t.Fatal(err)
	}
	sm, err := profiles.Build("edge-minimal", profiles.Config{StateMap: state})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerRoutes(mux, sm, explain.NewDisabled())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/edges", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("edges: %d %s", rec.Code, rec.Body.String())
	}
	var edges []EdgeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &edges); err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || !edges[0].Association || edges[0].Effective != nil || edges[0].Basis != "association" {
		t.Fatalf("graph lost association semantics: %+v", edges)
	}
}
