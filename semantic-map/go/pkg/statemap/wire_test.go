package statemap

import (
	"encoding/json"
	"os"
	"regexp"
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

// Relationships written under the old name still arrive — from a version 2 snapshot,
// the decisions in its journal, or a peer not yet upgraded — and must not be read as
// a recent strength of zero beside an observation count that says otherwise.
func TestRelationshipDecodesTheLegacyStrengthKey(t *testing.T) {
	var r Relationship
	if err := json.Unmarshal([]byte(`{"from":"a","to":"b","sign":1,"strength":0.42,`+
		`"n_observations":12,"effective":0.99,"basis":"asserted"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Recent != 0.42 || r.NObservations != 12 {
		t.Errorf("legacy decode gave recent %v from %d observations, want 0.42 from 12",
			r.Recent, r.NObservations)
	}
	if r.Assertion != nil {
		t.Error("a serialised basis of asserted must not invent an assertion; " +
			"effective and basis are derived, not restored")
	}

	if err := json.Unmarshal([]byte(`{"recent":0.3,"strength":0.8}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Recent != 0.3 {
		t.Errorf("recent = %v with both keys present, want the new key's 0.3", r.Recent)
	}
}

// A version 2 state file still loads, with the learned values and the decisions'
// recorded inputs intact.
func TestVersion2SnapshotStillLoads(t *testing.T) {
	m1, id := learnedPair(t)
	d := m1.Decide("dec-v2", "what drives b?")
	d.RelationshipsInto("b")
	d.Commit(map[string]any{"ok": true})
	learned, _ := m1.Relationship(id)

	path := t.TempDir() + "/state.json"
	if err := m1.Save(path); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite it as a version 2 agent would have written it.
	v2 := strings.ReplaceAll(string(blob), `"recent":`, `"strength":`)
	v2 = regexp.MustCompile(`"version":\s*3`).ReplaceAllString(v2, `"version": 2`)
	if strings.Contains(v2, `"recent":`) || !strings.Contains(v2, `"version": 2`) {
		t.Fatal("could not rewrite the snapshot as version 2; the test proves nothing")
	}
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}

	m2, _ := newTestMap(t, Config{ConvergenceObservations: 20, Learn: true})
	if _, err := m2.Load(path); err != nil {
		t.Fatalf("a version 2 snapshot was refused: %v", err)
	}
	got, _ := m2.Relationship(id)
	if got.Recent != learned.Recent || got.NObservations != learned.NObservations {
		t.Errorf("restored recent %.6f/%d, had %.6f/%d", got.Recent, got.NObservations,
			learned.Recent, learned.NObservations)
	}
	dec, ok := m2.Journal().Decision("dec-v2")
	if !ok || len(dec.RelationshipsRead) == 0 {
		t.Fatal("the decision's recorded relationships did not survive")
	}
	if dec.RelationshipsRead[0].Recent != learned.Recent {
		t.Errorf("decision input recent = %v, want %v: a replayed decision would show "+
			"a different input from the one it was made on",
			dec.RelationshipsRead[0].Recent, learned.Recent)
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
