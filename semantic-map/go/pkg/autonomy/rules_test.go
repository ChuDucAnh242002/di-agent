package autonomy_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/autonomy"
	"github.com/DiyazY/di-agent/pkg/semmap"
	"github.com/DiyazY/di-agent/pkg/statemap"
	"github.com/DiyazY/di-agent/pkg/types"
)

func TestPersistentCorrelationRule(t *testing.T) {
	for _, tc := range []struct {
		name       string
		windows    int
		undirected bool
		caveats    []string
		level      autonomy.Level
		wantAction string
		applied    bool
	}{
		{"legacy candidate", 0, false, nil, autonomy.Rules, types.ReviewUndecided, false},
		{"two windows", 2, false, nil, autonomy.Rules, types.ReviewUndecided, false},
		{"directed correlation is association-only", 3, false, nil, autonomy.Rules, types.ReviewConfirm, true},
		{"undirected", 3, true, nil, autonomy.Rules, types.ReviewConfirm, true},
		{"caveat", 3, false, []string{"missing range"}, autonomy.Rules, types.ReviewUndecided, false},
		{"suggest only", 3, false, nil, autonomy.Suggest, types.ReviewConfirm, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := directed("cpu_utilization@pod:a->cpu_pressure", "")
			c.PersistentWindows, c.Undirected, c.Caveats = tc.windows, tc.undirected, tc.caveats
			m := newFakeMap(t, c)
			cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"persistent-correlation"}})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Level = tc.level
			run(t, m, cfg)
			if c.Review == nil || c.Review.Action != tc.wantAction || c.Review.Applied != tc.applied {
				t.Fatalf("review %+v; want %s applied=%v", c.Review, tc.wantAction, tc.applied)
			}
			if tc.wantAction == types.ReviewConfirm {
				if !c.Review.Association {
					t.Fatalf("association flag does not match orientation: %+v", c.Review)
				}
				if c.Review.Model != "persistent-correlation" {
					t.Fatalf("wrong rule recorded: %+v", c.Review)
				}
				d, ok := m.st.Journal().Decision(c.Review.DecisionID)
				if !ok || d.Inputs["candidate"].(types.CandidateEdge).PersistentWindows != 3 {
					t.Fatalf("decision lacks persistence evidence: %+v", d)
				}
			}
		})
	}
}

func TestPersistenceProgressOutdatesOnlyAtApprovalThreshold(t *testing.T) {
	review := &types.CandidateReview{
		Direction: types.Positive, Kind: types.CandidateKindSameSubject,
		MIScore: 0.9, NObservations: 36, PersistentWindows: 1,
	}
	candidate := &types.CandidateEdge{
		Direction: types.Positive, Kind: types.CandidateKindSameSubject,
		MIScore: 0.9, NObservations: 36, PersistentWindows: 2,
	}
	if review.Outdated(candidate) {
		t.Fatal("ordinary persistence progress reopened the review")
	}
	candidate.PersistentWindows = types.PersistentWindowsRequired
	if !review.Outdated(candidate) {
		t.Fatal("crossing the persistence approval threshold did not reopen the review")
	}
	review.PersistentWindows = types.PersistentWindowsRequired
	candidate.PersistentWindows++
	if review.Outdated(candidate) {
		t.Fatal("additional windows after approval threshold reopened the review")
	}
	candidate.PersistentWindows = 0
	if !review.Outdated(candidate) {
		t.Fatal("a persistence reset below the approval threshold did not reopen the review")
	}
}

func TestPersistentCorrelationIsReviewedWhenWindowsAdvance(t *testing.T) {
	c := directed("cpu_utilization@pod:a->cpu_pressure", "")
	c.PersistentWindows = 1
	m := newFakeMap(t, c)
	cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"persistent-correlation"}})
	if err != nil {
		t.Fatal(err)
	}

	cfg.Level = autonomy.Rules
	r := run(t, m, cfg)
	first := c.Review.DecisionID
	for i := 0; i < 10; i++ {
		r.Cycle(context.Background())
	}
	if c.Review.DecisionID != first || c.Status != types.Pending {
		t.Fatal("repeated review cycles changed unchanged evidence")
	}
	c.PersistentWindows = 2
	r.Cycle(context.Background())
	if c.Review.DecisionID != first || c.Status != types.Pending {
		t.Fatal("ordinary persistence progress reopened the review or approved too soon")
	}
	c.PersistentWindows = 3
	r.Cycle(context.Background())
	if c.Status != types.Confirmed || c.Review.PersistentWindows != 3 || c.Review.DecisionID == first {
		t.Fatalf("unchanged r with new windows was not confirmed: %+v", c)
	}
}

func TestPersistentAssociationSurvivesRestartWithoutEnteringEstimates(t *testing.T) {
	const from, to = "load@engine:test", "speed@engine:test"
	build := func(state *statemap.Map) (*semmap.SemanticMap, *minimal.MICorrelationProposer) {
		p := minimal.NewMICorrelationProposer(state, 0.9, 6, 12, 0)
		m := semmap.New(minimal.NewOntologyFromSpec(nil), nil, p, minimal.NewDisabledTuner())
		m.AttachState(state)
		if _, err := m.RestoreDiscovery(time.Now(), 2*time.Minute); err != nil {
			t.Fatal(err)
		}
		return m, p
	}
	state := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	m, p := build(state)
	base := time.Now().Add(-time.Minute)
	feed := func(i int) {
		at := base.Add(time.Duration(i) * time.Second)
		x := float64(i%12) / 12
		for _, reading := range []types.PropertyReading{
			{ID: from, Subject: "engine:test", At: at, Value: x, Range: &[2]float64{0, 1}},
			{ID: to, Subject: "engine:test", At: at, Value: 0.1 + 0.8*x, Range: &[2]float64{0, 1}},
		} {
			if err := state.Record(statemap.Observation{ID: reading.ID, Subject: reading.Subject, Value: reading.Value, At: at}); err != nil {
				t.Fatal(err)
			}
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 29; i++ {
		feed(i)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	state = statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	if ok, err := state.Load(path); err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	m, p = build(state)
	cs, err := m.PendingCandidates()
	if err != nil || len(cs) != 1 || cs[0].PersistentWindows != 2 || cs[0].PersistencePairs != 5 {
		t.Fatalf("progress not restored: %+v %v", cs, err)
	}
	cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"persistent-correlation"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Level, cfg.Logf = autonomy.Rules, t.Logf
	reviewer, err := autonomy.New(m, cfg)
	if err != nil {
		t.Fatal(err)
	}
	reviewer.Cycle(context.Background())
	for i := 29; i < 36; i++ {
		if state.Associated(from, to, 1) {
			t.Fatal("association accepted before three complete windows")
		}
		feed(i)
		reviewer.Cycle(context.Background())
	}
	if !state.Associated(from, to, 1) || state.Covered(from, to, 1) || state.Covered(to, from, 1) {
		t.Fatal("association was not accepted independently of direction")
	}
	cs, err = m.CandidateHistory()
	if err != nil || len(cs) != 1 || cs[0].Status != types.Confirmed || cs[0].Review == nil || !cs[0].Review.Association {
		t.Fatalf("missing association review: %+v %v", cs, err)
	}
	d, ok := state.Journal().Decision(cs[0].Review.DecisionID)
	if !ok || d.Answer["association"] != true || d.Answer["applied"] != true {
		t.Fatalf("association decision is not auditable: %+v", d)
	}
	res := state.Estimate(statemap.EstimateRequest{Target: to, Assume: map[string]float64{from: 1}})
	if len(res.Influences) != 0 || res.Answer.Sensitivity != 0 {
		t.Fatalf("association entered estimate: %+v", res)
	}
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	state = statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	if ok, err := state.Load(path); err != nil || !ok {
		t.Fatalf("load confirmed association: %v %v", ok, err)
	}
	m, p = build(state)
	if cs, err := m.PendingCandidates(); err != nil || len(cs) != 0 || !state.Associated(from, to, 1) {
		t.Fatalf("accepted association became pending on restart: %+v %v", cs, err)
	}
}

func TestPersistentCorrelationCreatesATracedAssociationForDirectedPairs(t *testing.T) {
	const from, to = "cpu_utilization@pod:a", "cpu_pressure"
	state := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	p := minimal.NewMICorrelationProposer(state, 0.9, 6, 12, 0)
	m := semmap.New(minimal.NewOntologyFromSpec(nil), nil, p, minimal.NewDisabledTuner())
	m.AttachState(state)
	cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"composition", "persistent-correlation"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Level, cfg.Logf = autonomy.Rules, t.Logf
	r, err := autonomy.New(m, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rng := &[2]float64{0, 1}
	for i := 0; i < 36; i++ {
		at := time.Now().Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%12) / 12
		for _, reading := range []types.PropertyReading{
			{ID: from, Subject: "pod:a", At: at, Value: x, Range: rng},
			{ID: to, At: at, Value: 0.8*x + 0.1, Range: rng},
		} {
			if err := state.Record(statemap.Observation{ID: reading.ID, Subject: reading.Subject, Value: reading.Value, At: at}); err != nil {
				t.Fatal(err)
			}
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
			// A replay of the same timestamp must not supply another observation.
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
		}
		r.Cycle(context.Background())
		if i < 35 && state.Covered(from, to, 1) {
			t.Fatalf("confirmed after %d pairs, before three full windows", i+1)
		}
	}
	var rel statemap.Relationship
	ok := false
	for _, relationship := range state.Relationships("", "") {
		if relationship.Association && relationship.Label == "association" {
			rel, ok = relationship, true
			break
		}
	}
	if !ok || !rel.Association || rel.Provenance != statemap.Discovered ||
		!strings.Contains(rel.Note, "persistent-correlation") || state.Covered(from, to, 1) ||
		!state.Associated(from, to, 1) {
		t.Fatalf("missing traced association-only relationship: %+v", rel)
	}
	h, err := m.CandidateHistory()
	if err != nil || len(h) != 1 {
		t.Fatalf("history %v: %v", h, err)
	}
	c := h[0]
	if c.Status != types.Confirmed || c.Review == nil || c.Review.PersistentWindows != 3 ||
		!c.Review.Association {
		t.Fatalf("candidate not confirmed with persistence evidence: %+v", c)
	}
	d, ok := state.Journal().Decision(c.Review.DecisionID)
	if !ok || d.Answer["model"] != "persistent-correlation" || d.Answer["applied"] != true ||
		d.Answer["association"] != true {
		t.Fatalf("missing applied decision: %+v", d)
	}
}
