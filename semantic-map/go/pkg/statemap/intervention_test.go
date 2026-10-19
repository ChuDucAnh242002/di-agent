package statemap

import (
	"path/filepath"
	"strings"
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
	if r.Interventions == nil || r.Interventions.Null != 3 || r.Interventions.NullRun != 0 {
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

func TestRetiredRelationshipIsRecreatedByFreshAgreement(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, outcome(true, false, "d1"))
	mustRecord(t, m, outcome(true, false, "d2"))
	mustRecord(t, m, outcome(false, false, "d3"))
	mustRecord(t, m, outcome(false, false, "d4"))
	mustRecord(t, m, outcome(true, false, "d5"))
	id, changed := mustRecord(t, m, outcome(true, false, "d6"))
	if changed != "created" {
		t.Fatalf("changed %q", changed)
	}
	if r, _ := m.Relationship(id); r.Status != Active || r.Provenance != Intervened {
		t.Fatalf("relationship = %+v", r)
	}
	if c := m.Census(); c.Intervened != 1 {
		t.Fatalf("census intervened = %d", c.Intervened)
	}
}

func TestRedeclaredEdgeSurvivesOneNull(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, outcome(true, false, "d1"))
	mustRecord(t, m, outcome(true, false, "d2"))
	mustRecord(t, m, outcome(false, false, "d3"))
	mustRecord(t, m, outcome(false, false, "d4"))
	if err := m.DeclareRelationship(Relationship{From: "cpu_limit", To: "pressure", Sign: 1}); err != nil {
		t.Fatal(err)
	}
	if _, changed := mustRecord(t, m, outcome(false, false, "d5")); changed != "" {
		t.Fatalf("changed %q", changed)
	}
	if r, _ := m.Relationship("cpu_limit->pressure"); r.Status != Active {
		t.Fatalf("retired by one null")
	}
}

// TestAssertionIsNeverRetiredByInterventions: an operator's assertion outranks
// the agent's evidence either way. The nulls are still counted, and the run is
// reset so the next two do not retire it either.
func TestAssertionIsNeverRetiredByInterventions(t *testing.T) {
	m := interventionMap(t)
	if err := m.DeclareRelationship(Relationship{From: "cpu_limit", To: "pressure", Sign: 1, Provenance: Asserted}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"d1", "d2", "d3"} {
		if _, changed := mustRecord(t, m, outcome(false, false, d)); changed != "" {
			t.Fatalf("%s: changed %q", d, changed)
		}
	}
	r, _ := m.Relationship("cpu_limit->pressure")
	if r.Status != Active || r.Provenance != Asserted {
		t.Fatalf("relationship %+v; an assertion must stay", r)
	}
	if iv := r.Interventions; iv == nil || iv.Null != 3 || iv.NullRun != 1 {
		t.Fatalf("interventions %+v; want 3 nulls counted and the run reset after two", iv)
	}
}

func TestAssertionIsNeverOverwrittenByPromotion(t *testing.T) {
	m := interventionMap(t)
	if err := m.DeclareRelationship(Relationship{From: "cpu_limit", To: "pressure", Sign: 1, Provenance: Asserted}); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, m, outcome(true, false, "d1"))
	_, changed := mustRecord(t, m, outcome(true, false, "d2"))
	r, _ := m.Relationship("cpu_limit->pressure")
	if changed != "" || r.Provenance != Asserted {
		t.Fatalf("changed %q provenance %q", changed, r.Provenance)
	}
}

func measured(agreed, confounded bool, id string, effect float64) InterventionOutcome {
	o := outcome(agreed, confounded, id)
	o.Effect, o.HasEffect = effect, true
	return o
}

// TestAnInterventionsMeasuredEffectIsItsStrength: an action changed one thing and the
// map saw what followed, so its strength is the effect it measured — not the
// co-variation the learner folds in afterwards, which on a capped workload is small
// because the cap is what stopped the two moving together.
func TestAnInterventionsMeasuredEffectIsItsStrength(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, measured(true, false, "d1", 0.6))
	id, changed := mustRecord(t, m, measured(true, false, "d2", 0.8))
	if changed != "created" {
		t.Fatalf("changed %q, want created", changed)
	}
	for i := 0; i < 50; i++ {
		if err := m.ObserveRelationship(id, 0.05, m.now()); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := m.Relationship(id)
	if eff, ok := r.Effective(); !ok || !nearly(eff, 0.7) || r.Basis() != "measured" {
		t.Fatalf("effective %v (%v) basis %q; want the measured 0.7 over the co-observed 0.05", eff, ok, r.Basis())
	}
	// A null outcome measures a small effect and pulls the strength down.
	mustRecord(t, m, measured(false, false, "d3", 0.1))
	if r, _ = m.Relationship(id); !nearly(*r.Measured, 0.5) {
		t.Fatalf("measured %v; want the mean of 0.6, 0.8 and 0.1", *r.Measured)
	}
	// A confounded outcome measures nothing.
	mustRecord(t, m, measured(true, true, "d4", 1))
	if r, _ = m.Relationship(id); !nearly(*r.Measured, 0.5) {
		t.Fatalf("measured %v after a confounded outcome; want it unchanged", *r.Measured)
	}
	// An operator's assertion still outranks it.
	if err := m.AssertRelationshipStrength(id, 0.9, "op", "test"); err != nil {
		t.Fatal(err)
	}
	if r, _ = m.Relationship(id); r.Basis() != "asserted" {
		t.Fatalf("basis %q; an assertion outranks a measurement", r.Basis())
	}
}

// TestAMeasuredEffectReachesAPromotedRelationshipAndSurvivesARestart: a discovered
// relationship promoted by interventions takes their measured effect as well, and the
// measurement is part of the snapshot.
func TestAMeasuredEffectReachesAPromotedRelationshipAndSurvivesARestart(t *testing.T) {
	m := interventionMap(t)
	if err := m.DeclareRelationship(Relationship{From: "cpu_limit", To: "pressure", Sign: 1, Provenance: Discovered}); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, m, measured(true, false, "d1", 0.4))
	if _, changed := mustRecord(t, m, measured(true, false, "d2", 0.6)); changed != "promoted" {
		t.Fatalf("changed %q, want promoted", changed)
	}
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	m2, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	if ok, err := m2.Load(path); err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	r, _ := m2.Relationship("cpu_limit->pressure")
	if r.Measured == nil || !nearly(*r.Measured, 0.5) || r.Basis() != "measured" {
		t.Fatalf("after restart measured %v basis %q; want 0.5, measured", r.Measured, r.Basis())
	}
}

func nearly(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

// TestAnEstimateSaysWhatAMeasuredStrengthIs: a measured strength is a slope the
// interventions observed, clamped to 1, not a correlation magnitude, and a linear
// projection past the property's range is clipped; the decision says both, since a
// projection of 0 read without them looks like an exact answer.
func TestAnEstimateSaysWhatAMeasuredStrengthIs(t *testing.T) {
	m := interventionMap(t)
	for id, v := range map[string]float64{"cpu_limit": 0.5, "pressure": 0.2} {
		if err := m.Record(Observation{ID: id, Value: v, At: m.now()}); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord(t, m, measured(true, false, "d1", 1))
	mustRecord(t, m, measured(true, false, "d2", 1))
	res := m.Estimate(EstimateRequest{Target: "pressure", Assume: map[string]float64{"cpu_limit": 0}})
	if res.Err != "" || res.Hypothetical == nil || res.Hypothetical.ProjectedLevel != 0 {
		t.Fatalf("result %+v; want a projection clipped to 0", res)
	}
	all := strings.Join(res.Caveats, "\n")
	if strings.Contains(all, "correlation magnitude") {
		t.Errorf("caveats call a measured strength a correlation magnitude:\n%s", all)
	}
	for _, want := range []string{"measured by the agent's interventions", "at the clamp of 1, a lower bound", "clipped to the range"} {
		if !strings.Contains(all, want) {
			t.Errorf("caveats do not say %q:\n%s", want, all)
		}
	}
}
