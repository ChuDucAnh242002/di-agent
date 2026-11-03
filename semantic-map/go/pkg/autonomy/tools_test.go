package autonomy

import (
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

type toolStateMap struct {
	Map
	state *statemap.Map
}

func (m toolStateMap) State() *statemap.Map { return m.state }

func TestGetPropertySeparatesAssociationsFromInfluences(t *testing.T) {
	state := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	now := time.Now()
	for _, id := range []string{"tool-source", "tool-target"} {
		if err := state.Record(statemap.Observation{ID: id, Value: 0.5, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, relationship := range []statemap.Relationship{
		{From: "tool-source", To: "tool-target", Label: "causal", Sign: 1},
		{From: "tool-source", To: "tool-target", Label: "association", Sign: -1, Association: true},
	} {
		if err := state.DeclareRelationship(relationship); err != nil {
			t.Fatal(err)
		}
	}
	result, _, err := (getProperty{m: toolStateMap{state: state}}).Call(map[string]string{"property": "tool-source"})
	if err != nil {
		t.Fatal(err)
	}
	payload := result.(map[string]any)
	influences := payload["influences"].([]any)
	associations := payload["associations"].([]any)
	if len(influences) != 1 || len(associations) != 1 {
		t.Fatalf("relationships mixed across output categories: %+v", payload)
	}
	if influence, ok := influences[0].(statemap.Relationship); !ok || influence.Label != "causal" {
		t.Fatalf("directional output is not an influence: %#v", influences[0])
	}
	if association, ok := associations[0].(statemap.Relationship); !ok || !association.Association {
		t.Fatalf("association output is not marked symmetric: %#v", associations[0])
	}
}
