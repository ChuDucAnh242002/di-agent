package statemap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// learnedPair builds a map holding one relationship a→b with observations behind it.
func learnedPair(t *testing.T) (*Map, string) {
	t.Helper()
	m, c := newTestMap(t, Config{
		ConvergenceObservations: 20, Alpha: 0.5, Learn: true,
		LearnConfig: LearnConfig{PairWindowSeconds: 15, MinSupport: 6, Window: 30},
	})
	for _, id := range []string{"a", "b"} {
		if err := m.DeclareProperty(Property{ID: id, Range: [2]float64{0, 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DeclareRelationship(Relationship{From: "a", To: "b", Sign: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		c.advance(5 * time.Second)
		x := float64(i%10) / 10.0
		if err := m.ObserveEvent("a", x, c.now(), "a"+itoa(i)); err != nil {
			t.Fatal(err)
		}
		c.advance(time.Second)
		if err := m.ObserveEvent("b", 0.9*x, c.now(), "b"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	id := RelationshipID("a", "b", "")
	if r, _ := m.Relationship(id); r.NObservations == 0 {
		t.Fatal("nothing was learned, so the test proves nothing")
	}
	return m, id
}

// A relationship on the wire names its recent layer "recent", and carries the
// strength the agent reasons with and the layer that came from, so no reader has to
// apply the precedence rule itself.
func TestRelationshipJSONCarriesEffectiveAndBasis(t *testing.T) {
	m, id := learnedPair(t)
	if err := m.AssertRelationshipStrength(id, 0.7, "op", "test"); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Relationship(id)

	var got map[string]any
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["strength"]; ok {
		t.Error(`"strength" is still on the wire; it was the recent layer under a name ` +
			`that reads as the answer`)
	}
	if got["recent"] != r.Recent {
		t.Errorf("recent = %v, want %v", got["recent"], r.Recent)
	}
	if got["basis"] != "asserted" || got["effective"] != 0.7 {
		t.Errorf("effective %v from %v, want 0.7 from asserted: the assertion outranks "+
			"both learned layers", got["effective"], got["basis"])
	}
	if got["recent"] == got["effective"] {
		t.Error("the test needs recent to differ from effective to show they are separate")
	}

	// A relationship nothing has been observed about has no effective strength, which
	// is not the same claim as a strength of zero.
	unobserved, _ := json.Marshal(Relationship{From: "x", To: "y", Sign: 1})
	var u map[string]any
	_ = json.Unmarshal(unobserved, &u)
	if _, ok := u["effective"]; ok || u["basis"] != "unknown" {
		t.Errorf("unobserved relationship serialised effective=%v basis=%v; want no "+
			"effective and basis unknown", u["effective"], u["basis"])
	}
}

// A filter that matches nothing answers with empty lists, not null.
func TestEmptyStateViewSerialisesEmptyLists(t *testing.T) {
	m, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	b, err := json.Marshal(m.State(Query{Subject: "pod:gone"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"properties":[]`, `"relationships":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("empty view lacks %s: %s", want, b)
		}
	}
}
