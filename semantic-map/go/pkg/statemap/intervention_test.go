package statemap

import (
	"encoding/json"
	"os"
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

// TestARelationshipRetiredByInterventionsMeasuresAfresh: two null outcomes retire a
// relationship because acting had no effect; one later created again on the pair is
// a new claim and reasons from the outcomes that created it, not from a mean that
// still holds the effects before the retirement and the nulls that caused it.
func TestARelationshipRetiredByInterventionsMeasuresAfresh(t *testing.T) {
	m := interventionMap(t)
	mustRecord(t, m, measured(true, false, "d1", 1))
	mustRecord(t, m, measured(true, false, "d2", 1))
	mustRecord(t, m, measured(false, false, "d3", 0))
	if _, changed := mustRecord(t, m, measured(false, false, "d4", 0)); changed != "retired" {
		t.Fatalf("changed %q; want retired", changed)
	}
	mustRecord(t, m, measured(true, false, "d5", 0.4))
	id, changed := mustRecord(t, m, measured(true, false, "d6", 0.4))
	if changed != "created" {
		t.Fatalf("changed %q; want created again", changed)
	}
	if r, _ := m.Relationship(id); r.Measured == nil || !nearly(*r.Measured, 0.4) {
		t.Fatalf("measured %v; want 0.4, the outcomes since the retirement", r.Measured)
	}
	iv := m.interventions[interventionKey("cpu_limit", "pressure")]
	if iv.SlopeN != 2 || !nearly(iv.SlopeSum, 0.8) {
		t.Fatalf("slopes %+v; want only the two outcomes since retirement", iv)
	}
}

func TestLiveSlopeIgnoresRetiredRelationshipAndRestoresLiveEvidence(t *testing.T) {
	m, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	from, to := "memory_utilization@pod:gone", "memory_contention_ratio"
	for _, p := range []Property{
		{ID: from, Kind: Observed, Unit: "fraction", Subject: "pod:gone",
			Range: [2]float64{0, 1}, Source: "test"},
		{ID: to, Kind: Observed, Unit: "fraction", Range: [2]float64{0, 1}, Source: "test"},
	} {
		if err := m.DeclareProperty(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, outcome := range []InterventionOutcome{
		{From: from, To: to, Agreed: true, HasEffect: true, Effect: 0.4, DecisionID: "verify-1"},
		{From: from, To: to, Agreed: true, HasEffect: true, Effect: 0.6, DecisionID: "verify-2"},
	} {
		if _, _, err := m.RecordIntervention(outcome); err != nil {
			t.Fatal(err)
		}
	}

	if estimate, ok := m.LiveSlope("memory_utilization", to); !ok ||
		!nearly(estimate.Estimate(), 1.0/3) || estimate.N != 2 ||
		estimate.LastSource != from || estimate.LastDecision != "verify-2" {
		t.Fatalf("domain effect %+v (%v); want a shrunken estimate from the live source", estimate, ok)
	}
	if err := m.RetireProperty(from, "subject left", "test"); err != nil {
		t.Fatal(err)
	}
	if estimate, ok := m.LiveSlope("memory_utilization", to); ok {
		t.Fatalf("retired source still informs new decisions: %+v", estimate)
	}

	path := filepath.Join(t.TempDir(), "snap.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	m2, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	if ok, err := m2.Load(path); err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if estimate, ok := m2.LiveSlope("memory_utilization", to); ok {
		t.Fatalf("restored retired source still informs new decisions: %+v", estimate)
	}
	if err := m2.Record(Observation{ID: from, Subject: "pod:gone", Value: 0.2, At: m2.now()}); err != nil {
		t.Fatal(err)
	}
	if err := m2.DeclareRelationship(Relationship{From: from, To: to, Sign: 1}); err != nil {
		t.Fatal(err)
	}
	if got, ok := m2.LiveSlope("memory_utilization", to); !ok ||
		got.N != 2 || !nearly(got.Estimate(), 1.0/3) || got.LastDecision != "verify-2" {
		t.Fatalf("freshly observed and revived source slope %+v (%t); want retained evidence", got, ok)
	}

	unsupported := m.Snapshot()
	unsupported.Version = 5
	raw, err := json.Marshal(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(t.TempDir(), "old-snap.json")
	if err := os.WriteFile(oldPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m3, _ := newTestMap(t, Config{ConvergenceObservations: 10, Alpha: 0.5})
	if ok, err := m3.Load(oldPath); err == nil || ok || !strings.Contains(err.Error(), "snapshot is version 5") {
		t.Fatalf("load version 5 snapshot = %t, %v; want explicit refusal", ok, err)
	}
}

func TestLiveSlopeSelection(t *testing.T) {
	const usage, target = "memory_utilization", "pressure"
	for _, tc := range []struct {
		name        string
		source      string
		status      Status
		sign        int
		association bool
		retired     bool
		effect      float64
		wantSource  string
		wantSlope   float64
	}{
		{name: "strongest live", sign: 1, effect: 1.2, wantSource: usage + "@pod:a", wantSlope: 0.6},
		{name: "stale source", status: Stale, sign: 1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "retired source", status: Retired, sign: 1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "association", association: true, sign: 1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "negative sign", sign: -1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "prefix collision", source: usage + "_x@pod:a", sign: 1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "retired relationship", retired: true, sign: 1, effect: 1.2, wantSource: usage + "@pod:b", wantSlope: 0.2},
		{name: "tie breaks by source", sign: 1, effect: 0.4, wantSource: usage + "@pod:a", wantSlope: 0.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestMap(t, Config{})
			from := tc.source
			if from == "" {
				from = usage + "@pod:a"
			}
			for _, id := range []string{target, from, usage + "@pod:b"} {
				_, subject, _ := strings.Cut(id, "@")
				if err := m.DeclareProperty(Property{ID: id, Subject: subject, Kind: Observed, Range: [2]float64{0, 1}}); err != nil {
					t.Fatal(err)
				}
				if err := m.Record(Observation{ID: id, Subject: subject, Value: 0.1, At: m.now()}); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range []Relationship{
				{From: usage + "@pod:b", To: target, Sign: 1},
				{From: from, To: target, Sign: tc.sign, Association: tc.association},
			} {
				if err := m.DeclareRelationship(r); err != nil {
					t.Fatal(err)
				}
			}
			mustRecord(t, m, InterventionOutcome{
				From: usage + "@pod:b", To: target, Agreed: true, HasEffect: true, Effect: 0.4,
			})
			mustRecord(t, m, InterventionOutcome{
				From: from, To: target, Agreed: true, HasEffect: true, Effect: tc.effect,
			})
			switch tc.status {
			case Stale:
				if !m.MarkPropertyStale(from, "test", "test") {
					t.Fatal("source was not marked stale")
				}
			case Retired:
				if err := m.RetireProperty(from, "test", "test"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.retired {
				if err := m.RetireRelationship(RelationshipID(from, target, ""), "test", "test"); err != nil {
					t.Fatal(err)
				}
			}
			for range 20 {
				got, ok := m.LiveSlope(usage, target)
				if !ok || got.LastSource != tc.wantSource || !nearly(got.Estimate(), tc.wantSlope) {
					t.Fatalf("slope %+v (%t); want %s at %v", got, ok, tc.wantSource, tc.wantSlope)
				}
			}
			if tc.status == Stale {
				if err := m.Record(Observation{ID: from, Subject: "pod:a", Value: 0.2, At: m.now()}); err != nil {
					t.Fatal(err)
				}
				if got, ok := m.LiveSlope(usage, target); !ok || got.LastSource != from {
					t.Fatalf("fresh source did not restore its evidence: %+v (%t)", got, ok)
				}
			}
		})
	}
}

func TestInterventionSlopeCanExceedOne(t *testing.T) {
	m, _ := newTestMap(t, Config{})
	from, to := "memory_utilization@pod:hog", "memory_contention_ratio"
	for _, p := range []Property{
		{ID: from, Kind: Observed, Subject: "pod:hog", Unit: "fraction", Range: [2]float64{0, 1}},
		{ID: to, Kind: Observed, Unit: "fraction", Range: [2]float64{0, 1}},
	} {
		if err := m.DeclareProperty(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DeclareRelationship(Relationship{From: from, To: to, Sign: 1}); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, m, InterventionOutcome{
		From: from, To: to, Agreed: true, HasEffect: true, Effect: 4.8, DecisionID: "verify-sharp-response",
	})
	e, ok := m.LiveSlope("memory_utilization", to)
	if !ok || !nearly(e.Estimate(), 2) {
		t.Fatalf("domain slope %+v (%t); want capped slope 4 shrunk to 2", e, ok)
	}
	if pair, ok := m.interventions[interventionKey(from, to)]; !ok || pair.EffectSum != 1 {
		t.Fatalf("pair effect %+v (%t); want relationship strength kept within [0,1]", pair, ok)
	}
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	restored, _ := newTestMap(t, Config{})
	if ok, err := restored.Load(path); err != nil || !ok {
		t.Fatalf("load: %t, %v", ok, err)
	}
	if got, ok := restored.LiveSlope("memory_utilization", to); !ok ||
		got.EffectSum != e.EffectSum || got.N != e.N || got.LastSource != e.LastSource ||
		got.LastDecision != e.LastDecision || !got.Last.Equal(e.Last) {
		t.Fatalf("restored slope %+v (%t); want %+v", got, ok, e)
	}
	id := RelationshipID(from, to, "")
	if err := restored.RetireRelationship(id, "test", "test"); err != nil {
		t.Fatal(err)
	}
	if got, ok := restored.LiveSlope("memory_utilization", to); ok {
		t.Fatalf("retired relationship still selected: %+v", got)
	}
	if err := restored.DeclareRelationship(Relationship{From: from, To: to, Sign: 1}); err != nil {
		t.Fatal(err)
	}
	if r, ok := restored.Relationship(id); !ok || r.Measured == nil || !nearly(*r.Measured, 1) {
		t.Fatalf("revived relationship %+v (%t); want retained strength alongside slope", r, ok)
	}
	if got, ok := restored.LiveSlope("memory_utilization", to); !ok ||
		got.N != e.N || !nearly(got.Estimate(), e.Estimate()) {
		t.Fatalf("revived relationship slope %+v (%t); want retained evidence %+v", got, ok, e)
	}
	mustRecord(t, restored, InterventionOutcome{
		From: from, To: to, Agreed: true, HasEffect: true, Effect: 0.4, DecisionID: "fresh",
	})
	if got, ok := restored.LiveSlope("memory_utilization", to); !ok ||
		got.N != 2 || !nearly(got.Estimate(), 4.4/3) || got.LastDecision != "fresh" {
		t.Fatalf("revived relationship slope %+v (%t); want retained and fresh evidence", got, ok)
	}
}
