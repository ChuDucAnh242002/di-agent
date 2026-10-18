package statemap

import (
	"path/filepath"
	"testing"
)

func interventionMap(t *testing.T) *Map {
	t.Helper()
	m, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	for _, id := range []string{"cpu_limit", "pressure"} {
		if err := m.DeclareProperty(Property{ID: id, Kind: Observed, Unit: "fraction",
			Range: [2]float64{0, 1}, Source: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func outcome(agreed, confounded bool, id string) InterventionOutcome {
	return InterventionOutcome{From: "cpu_limit", To: "pressure", Agreed: agreed, Confounded: confounded, DecisionID: id}
}

func mustRecord(t *testing.T, m *Map, o InterventionOutcome) (string, string) {
	t.Helper()
	id, changed, err := m.RecordIntervention(o)
	if err != nil {
		t.Fatal(err)
	}
	return id, changed
}

func TestTwoAgreeingInterventionsCreateARelationship(t *testing.T) {
	m := interventionMap(t)
	if _, changed := mustRecord(t, m, outcome(true, false, "d1")); changed != "" {
		t.Fatalf("first agreement changed %q", changed)
	}
	id, changed := mustRecord(t, m, outcome(true, false, "d2"))
	if changed != "created" || id != "cpu_limit->pressure" {
		t.Fatalf("got id %q changed %q", id, changed)
	}
	r, ok := m.Relationship(id)
	if !ok || r.Provenance != Intervened || r.Sign != 1 || r.Status != Active {
		t.Fatalf("relationship = %+v", r)
	}
	if r.Interventions == nil || r.Interventions.Agreed != 2 || r.Interventions.LastDecision != "d2" {
		t.Fatalf("interventions = %+v", r.Interventions)
	}
}

func TestOneIsNotEnough(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, outcome(true, false, "d1"))
	if got := m.Relationships("", ""); len(got) != 0 {
		t.Fatalf("one agreement made %d relationships", len(got))
	}
}

func TestAgreementPromotesADiscoveredRelationship(t *testing.T) {
	m := interventionMap(t)
	if err := m.DeclareRelationship(Relationship{From: "cpu_limit", To: "pressure", Sign: 1, Provenance: Discovered}); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, m, outcome(true, false, "d1"))
	id, changed := mustRecord(t, m, outcome(true, false, "d2"))
	if changed != "promoted" {
		t.Fatalf("changed %q", changed)
	}
	if r, _ := m.Relationship(id); r.Provenance != Intervened {
		t.Fatalf("provenance %q", r.Provenance)
	}
}

func TestTwoNullsRetireIt(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, outcome(true, false, "d1"))
	mustRecord(t, m, outcome(true, false, "d2"))
	mustRecord(t, m, outcome(false, false, "d3"))
	// An agreement resets the run of nulls.
	mustRecord(t, m, outcome(true, false, "d4"))
	mustRecord(t, m, outcome(false, false, "d5"))
	if r, _ := m.Relationship("cpu_limit->pressure"); r.Status != Active {
		t.Fatalf("retired after non-consecutive nulls")
	}
	id, changed := mustRecord(t, m, outcome(false, false, "d6"))
	if changed != "retired" {
		t.Fatalf("changed %q", changed)
	}
	r, _ := m.Relationship(id)
	if r.Status != Retired || r.RetiredReason != "two interventions had no effect" {
		t.Fatalf("relationship = %+v", r)
	}
	if r.Interventions == nil || r.Interventions.Null != 3 || r.Interventions.NullRun != 2 {
		t.Fatalf("interventions = %+v", r.Interventions)
	}
}

func TestConfoundedOutcomesChangeNothing(t *testing.T) {
	m := interventionMap(t)
	for _, d := range []string{"d1", "d2", "d3"} {
		if _, changed := mustRecord(t, m, outcome(true, true, d)); changed != "" {
			t.Fatalf("confounded outcome changed %q", changed)
		}
	}
	if got := m.Relationships("", ""); len(got) != 0 {
		t.Fatalf("confounded outcomes made %d relationships", len(got))
	}
	mustRecord(t, m, outcome(true, false, "d4"))
	mustRecord(t, m, outcome(true, false, "d5"))
	r, _ := m.Relationship("cpu_limit->pressure")
	if r.Interventions.Confounded != 3 || r.Interventions.Agreed != 2 {
		t.Fatalf("interventions = %+v", r.Interventions)
	}
}

func TestInterventionsSurviveARestart(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, outcome(true, false, "d1"))
	mustRecord(t, m, outcome(false, true, "d2"))
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	// Load needs an empty map.
	m2, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	if ok, err := m2.Load(path); err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	_, changed := mustRecord(t, m2, outcome(true, false, "d3"))
	if changed != "created" {
		t.Fatalf("second agreement after restart changed %q: counts were lost", changed)
	}
	r, _ := m2.Relationship("cpu_limit->pressure")
	if r.Interventions.Confounded != 1 || r.Interventions.Agreed != 2 {
		t.Fatalf("interventions = %+v", r.Interventions)
	}
}
